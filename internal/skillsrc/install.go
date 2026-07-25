package skillsrc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type ownership struct {
	Version                int    `json:"version"`
	Owner                  string `json:"owner"`
	Skill                  string `json:"skill"`
	SourceHash             string `json:"source_hash,omitempty"`
	InstalledHash          string `json:"installed_hash,omitempty"`
	DisableModelInvocation bool   `json:"disable_model_invocation"`
}

type transaction struct {
	Version int    `json:"version"`
	Action  string `json:"action"`
	Skill   string `json:"skill"`
	Temp    string `json:"temp,omitempty"`
	Backup  string `json:"backup,omitempty"`
}

type installer struct {
	target           string
	manifestLockPath string
	lockDir          string
	owner            string
}

func newInstaller(target, manifestPath, lockDir string) *installer {
	absolute, _ := filepath.Abs(manifestPath)
	clean := filepath.Clean(absolute)
	return &installer{
		target:           target,
		manifestLockPath: filepath.Dir(clean),
		lockDir:          lockDir,
		owner:            sha256String(clean),
	}
}

func (installer *installer) withLock(ctx context.Context, operation func() error) error {
	targetParent, err := filepath.Abs(filepath.Dir(installer.target))
	if err != nil {
		return fmt.Errorf("resolve install target: %w", err)
	}
	if err := os.MkdirAll(targetParent, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(installer.lockDir, 0o755); err != nil {
		return fmt.Errorf("create lock cache: %w", err)
	}
	lockPaths := []string{cacheLockPath(installer.lockDir, installer.manifestLockPath)}
	targetLock := cacheLockPath(installer.lockDir, filepath.Clean(targetParent))
	if targetLock != lockPaths[0] {
		lockPaths = append(lockPaths, targetLock)
		sort.Strings(lockPaths)
	}
	var unlocks []func()
	for _, lockPath := range lockPaths {
		unlock, err := lockFileContext(ctx, lockPath)
		if err != nil {
			for index := len(unlocks) - 1; index >= 0; index-- {
				unlocks[index]()
			}
			return err
		}
		unlocks = append(unlocks, unlock)
	}
	defer func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
	}()
	if err := os.MkdirAll(installer.target, 0o755); err != nil {
		return fmt.Errorf("create install target: %w", err)
	}
	if err := installer.recoverTransactions(); err != nil {
		return err
	}
	return operation()
}

func cacheLockPath(lockDir, resource string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(resource)))
	return filepath.Join(lockDir, hex.EncodeToString(digest[:])+".lock")
}

func (installer *installer) install(ctx context.Context, name, sourceDir, expectedHash string, disableModelInvocation bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	destination := filepath.Join(installer.target, name)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unmanaged collision at %q", destination)
		}
		managed, markerErr := installer.managed(destination, name)
		if markerErr != nil || !managed {
			return fmt.Errorf("unmanaged collision at %q", destination)
		}
		if installedStatus(installer, installer.target, LockedSkill{Name: name, Hash: expectedHash}, disableModelInvocation) == "current" {
			return nil
		}
	}

	staging, err := os.MkdirTemp(installer.target, "."+name+".skillsrc-tmp-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := copySkill(ctx, sourceDir, staging); err != nil {
		return err
	}
	actualHash, err := HashSkill(staging)
	if err != nil {
		return err
	}
	if actualHash != expectedHash {
		return fmt.Errorf("skill %q changed while being copied: got %s, expected %s", name, actualHash, expectedHash)
	}
	if disableModelInvocation {
		if err := setDisableModelInvocation(filepath.Join(staging, "SKILL.md")); err != nil {
			return fmt.Errorf("skill %q: %w", name, err)
		}
	}
	installedHash, err := HashSkill(staging)
	if err != nil {
		return err
	}
	marker := ownership{Version: SchemaVersion, Owner: installer.owner, Skill: name, SourceHash: expectedHash, InstalledHash: installedHash, DisableModelInvocation: disableModelInvocation}
	markerBytes, _ := json.Marshal(marker)
	markerBytes = append(markerBytes, '\n')
	if err := writeFileSynced(filepath.Join(staging, ownershipFile), markerBytes, 0o644); err != nil {
		return fmt.Errorf("write ownership marker: %w", err)
	}
	if err := syncDirectory(staging); err != nil {
		return err
	}

	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, destination); err != nil {
			return fmt.Errorf("publish skill %q: %w", name, err)
		}
		cleanupStaging = false
		return syncDirectory(installer.target)
	}

	backup := filepath.Join(installer.target, "."+name+".skillsrc-old-"+strconvTime())
	journalPath := filepath.Join(installer.target, "."+name+".skillsrc-txn.json")
	journal := transaction{Version: SchemaVersion, Action: "replace", Skill: name, Temp: filepath.Base(staging), Backup: filepath.Base(backup)}
	if err := writeJSONAtomic(journalPath, journal); err != nil {
		return err
	}
	if err := os.Rename(destination, backup); err != nil {
		_ = os.Remove(journalPath)
		return fmt.Errorf("move current skill %q aside: %w", name, err)
	}
	if err := os.Rename(staging, destination); err != nil {
		restoreErr := os.Rename(backup, destination)
		if restoreErr == nil {
			_ = os.Remove(journalPath)
		}
		return fmt.Errorf("publish replacement for %q: %w (restore: %v)", name, err, restoreErr)
	}
	cleanupStaging = false
	if err := syncDirectory(installer.target); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove backup for %q: %w", name, err)
	}
	if err := os.Remove(journalPath); err != nil {
		return fmt.Errorf("remove transaction journal for %q: %w", name, err)
	}
	return syncDirectory(installer.target)
}

func (installer *installer) prune(name string) error {
	destination := filepath.Join(installer.target, name)
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to prune unowned path %q", destination)
	}
	managed, markerErr := installer.managed(destination, name)
	if markerErr != nil || !managed {
		return fmt.Errorf("refusing to prune %q without valid skillsrc ownership", destination)
	}
	backup := filepath.Join(installer.target, "."+name+".skillsrc-prune-"+strconvTime())
	journalPath := filepath.Join(installer.target, "."+name+".skillsrc-txn.json")
	if err := writeJSONAtomic(journalPath, transaction{Version: SchemaVersion, Action: "prune", Skill: name, Backup: filepath.Base(backup)}); err != nil {
		return err
	}
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("stage prune of %q: %w", name, err)
	}
	if err := syncDirectory(installer.target); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	return syncDirectory(installer.target)
}

func (installer *installer) managed(dir, name string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ownershipFile))
	if err != nil {
		return false, err
	}
	var marker ownership
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, err
	}
	return marker.Version == SchemaVersion && marker.Owner == installer.owner && marker.Skill == name, nil
}

func (installer *installer) recoverTransactions() error {
	entries, err := os.ReadDir(installer.target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".skillsrc-txn.json") {
			continue
		}
		path := filepath.Join(installer.target, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read transaction journal %q: %w", path, err)
		}
		var journal transaction
		if err := json.Unmarshal(data, &journal); err != nil || journal.Version != SchemaVersion || !skillNamePattern.MatchString(journal.Skill) || entry.Name() != "."+journal.Skill+".skillsrc-txn.json" {
			return fmt.Errorf("invalid transaction journal %q", path)
		}
		if journal.Action == "replace" {
			if !generatedForSkill(journal.Temp, journal.Skill, "tmp") || !generatedForSkill(journal.Backup, journal.Skill, "old") {
				return fmt.Errorf("invalid transaction paths in %q", path)
			}
		} else if journal.Action == "prune" {
			if journal.Temp != "" || !generatedForSkill(journal.Backup, journal.Skill, "prune") {
				return fmt.Errorf("invalid transaction paths in %q", path)
			}
		} else {
			return fmt.Errorf("invalid transaction action in %q", path)
		}
		destination := filepath.Join(installer.target, journal.Skill)
		temporary := filepath.Join(installer.target, journal.Temp)
		backup := filepath.Join(installer.target, journal.Backup)
		_, destinationErr := os.Lstat(destination)
		if journal.Action == "prune" {
			if destinationErr == nil {
				return fmt.Errorf("cannot recover prune for %q: destination reappeared", journal.Skill)
			}
			_ = os.RemoveAll(backup)
			_ = os.Remove(path)
			continue
		}
		if destinationErr == nil {
			_ = os.RemoveAll(temporary)
			_ = os.RemoveAll(backup)
			_ = os.Remove(path)
			continue
		}
		if _, err := os.Lstat(temporary); err == nil {
			if err := os.Rename(temporary, destination); err != nil {
				return fmt.Errorf("recover replacement for %q: %w", journal.Skill, err)
			}
			_ = os.RemoveAll(backup)
			_ = os.Remove(path)
			continue
		}
		if _, err := os.Lstat(backup); err == nil {
			if err := os.Rename(backup, destination); err != nil {
				return fmt.Errorf("restore backup for %q: %w", journal.Skill, err)
			}
			_ = os.Remove(path)
			continue
		}
		return fmt.Errorf("cannot recover transaction for %q", journal.Skill)
	}
	entries, err = os.ReadDir(installer.target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(installer.target, name)
		if skill, ok := generatedArtifact(name, "tmp"); ok {
			managed, markerErr := installer.managed(path, skill)
			if markerErr != nil || !managed {
				return fmt.Errorf("orphan staging directory requires inspection: %q", path)
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("clean orphan staging directory %q: %w", path, err)
			}
		}
		if _, ok := generatedArtifact(name, "old"); ok {
			return fmt.Errorf("orphan install backup requires inspection: %q", path)
		}
		if _, ok := generatedArtifact(name, "prune"); ok {
			return fmt.Errorf("orphan install backup requires inspection: %q", path)
		}
	}
	return syncDirectory(installer.target)
}

func generatedForSkill(name, skill, kind string) bool {
	prefix := "." + skill + ".skillsrc-" + kind + "-"
	return filepath.Base(name) == name && strings.HasPrefix(name, prefix) && len(name) > len(prefix)
}

func generatedArtifact(name, kind string) (string, bool) {
	if !strings.HasPrefix(name, ".") {
		return "", false
	}
	marker := ".skillsrc-" + kind + "-"
	index := strings.LastIndex(name[1:], marker)
	if index <= 0 {
		return "", false
	}
	skill := name[1 : index+1]
	return skill, skillNamePattern.MatchString(skill) && generatedForSkill(name, skill, kind)
}

func copySkill(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ownershipFile {
			return &ValidationError{Problem: fmt.Sprintf("source contains reserved ownership file %q", ownershipFile)}
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm()&0o777)
		}
		resolved, info, err := safeFilePath(source, path)
		if err != nil {
			return err
		}
		input, err := os.Open(resolved)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o777)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		return closeInErr
	})
}

func setDisableModelInvocation(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read SKILL.md mode: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read SKILL.md frontmatter: %w", err)
	}
	separator := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		separator = "\r\n"
	}
	lines := strings.Split(string(data), separator)
	if len(lines) < 2 || lines[0] != "---" {
		return &ValidationError{Problem: "disable-model-invocation requires SKILL.md to start with YAML frontmatter"}
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return &ValidationError{Problem: "disable-model-invocation requires a closing YAML frontmatter delimiter in SKILL.md"}
	}
	found := false
	for i := 1; i < closing; i++ {
		const key = "disable-model-invocation"
		if strings.HasPrefix(lines[i], key) {
			rest := strings.TrimSpace(strings.TrimPrefix(lines[i], key))
			if strings.HasPrefix(rest, ":") {
				lines[i] = key + ": true"
				found = true
			}
		}
	}
	if !found {
		lines = append(lines[:closing], append([]string{"disable-model-invocation: true"}, lines[closing:]...)...)
	}
	return writeAtomic(path, []byte(strings.Join(lines, separator)), info.Mode().Perm())
}

func lockFileContext(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'), 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == string(data) {
		return nil
	}
	temporaryPath, err := writeSyncedTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeAtomicNew(path string, data []byte, mode os.FileMode) error {
	temporaryPath, err := writeSyncedTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	// Linking publishes the fully synced file atomically and, unlike Rename,
	// fails when another initializer has already created the destination.
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeSyncedTemp(path string, data []byte, mode os.FileMode) (string, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".skillsrc-write-")
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	remove = false
	return temporary.Name(), nil
}

func writeFileSynced(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
