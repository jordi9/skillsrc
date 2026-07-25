package skillsrc

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Repository struct {
	Identity string
	URL      string
}

type GitError struct {
	Args   []string
	Output string
	Err    error
}

func (e *GitError) Error() string {
	output := strings.TrimSpace(e.Output)
	if output == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, output)
}

func (e *GitError) Unwrap() error { return e.Err }

type acquiredRepository struct {
	dir     string
	fetched bool
}

type GitOperation struct {
	cacheDir string
	git      string
	acquired map[string]*acquiredRepository
	fetches  []FetchEvent
	cleaned  bool
}

func NewGitOperation(cacheDir, gitBinary string) *GitOperation {
	if gitBinary == "" {
		gitBinary = "git"
	}
	return &GitOperation{cacheDir: cacheDir, git: gitBinary, acquired: make(map[string]*acquiredRepository)}
}

func (operation *GitOperation) Fetches() []FetchEvent {
	return append([]FetchEvent(nil), operation.fetches...)
}

func (operation *GitOperation) Resolve(ctx context.Context, rawRepo, ref string, refresh bool, lockedCommit string) (string, error) {
	repository, err := NormalizeRepository(rawRepo)
	if err != nil {
		return "", err
	}
	unlock, err := operation.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()

	acquired, err := operation.repository(ctx, repository)
	if err != nil {
		return "", err
	}
	if !refresh && lockedCommit != "" {
		if err := validateCommitID(lockedCommit); err != nil {
			return "", err
		}
		if operation.hasCommit(ctx, acquired.dir, lockedCommit) {
			return lockedCommit, nil
		}
		if err := operation.fetchExact(ctx, acquired, lockedCommit); err != nil {
			return "", fmt.Errorf("fetch locked commit %s from %s: %w", lockedCommit, rawRepo, err)
		}
		operation.fetches = append(operation.fetches, FetchEvent{Source: rawRepo, Reason: "locked commit missing from cache", Commit: lockedCommit})
		return lockedCommit, nil
	}

	if isCommitID(ref) && !operation.hasCommit(ctx, acquired.dir, ref) {
		if err := operation.fetchExact(ctx, acquired, ref); err != nil {
			return "", fmt.Errorf("acquire exact commit from %s: %w", rawRepo, err)
		}
		operation.fetches = append(operation.fetches, FetchEvent{Source: rawRepo, Reason: "exact configured commit missing from cache", Commit: ref})
	} else if !isCommitID(ref) && !acquired.fetched {
		if err := operation.fetchAll(ctx, acquired); err != nil {
			return "", fmt.Errorf("acquire %s: %w", rawRepo, err)
		}
		reason := "new or changed declaration"
		if lockedCommit != "" {
			reason = "update configured ref"
		}
		operation.fetches = append(operation.fetches, FetchEvent{Source: rawRepo, Reason: reason})
	}
	commit, err := operation.resolveFetched(ctx, acquired.dir, ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s ref %q: %w", rawRepo, ref, err)
	}
	if _, err := operation.run(ctx, "--git-dir="+acquired.dir, "update-ref", "refs/skillsrc/pins/"+commit, commit); err != nil {
		return "", fmt.Errorf("pin commit %s: %w", commit, err)
	}
	return commit, nil
}

func (operation *GitOperation) Materialize(ctx context.Context, rawRepo, commit, destination string) error {
	repository, err := NormalizeRepository(rawRepo)
	if err != nil {
		return err
	}
	unlock, err := operation.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	acquired, err := operation.repository(ctx, repository)
	if err != nil {
		return err
	}
	if !operation.hasCommit(ctx, acquired.dir, commit) {
		return fmt.Errorf("commit %s is absent from cache", commit)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create materialization directory: %w", err)
	}
	command := exec.CommandContext(ctx, operation.git, "--git-dir="+acquired.dir, "archive", "--format=tar", commit)
	command.Env = gitEnvironment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open git archive output: %w", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start git archive: %w", err)
	}
	extractErr := extractTar(ctx, stdout, destination)
	if extractErr != nil {
		_ = stdout.Close() // unblock git if validation stopped before consuming the archive
	}
	waitErr := command.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return &GitError{Args: command.Args[1:], Output: stderr.String(), Err: waitErr}
	}
	return nil
}

func NormalizeRepository(raw string) (Repository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") {
		return Repository{}, &ValidationError{Problem: fmt.Sprintf("invalid repository %q", raw)}
	}
	shorthand := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	if shorthand.MatchString(raw) {
		path := strings.TrimSuffix(raw, ".git")
		return Repository{Identity: "github.com/" + strings.ToLower(path), URL: "https://github.com/" + path + ".git"}, nil
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		rest := strings.TrimPrefix(raw, "git@")
		separator := strings.IndexAny(rest, ":/")
		if separator <= 0 || separator == len(rest)-1 {
			return Repository{}, &ValidationError{Problem: fmt.Sprintf("invalid SSH repository %q", raw)}
		}
		host, path := rest[:separator], rest[separator+1:]
		path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
		if host == "" || path == "" {
			return Repository{}, &ValidationError{Problem: fmt.Sprintf("invalid SSH repository %q", raw)}
		}
		return Repository{Identity: strings.ToLower(host) + "/" + strings.ToLower(path), URL: "ssh://git@" + host + "/" + path + ".git"}, nil
	}
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "ssh") && parsed.Hostname() != "" {
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return Repository{}, &ValidationError{Problem: "repository URLs must not contain passwords"}
			}
		}
		path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
		if path == "" {
			return Repository{}, &ValidationError{Problem: fmt.Sprintf("invalid repository %q", raw)}
		}
		return Repository{Identity: strings.ToLower(parsed.Hostname()) + "/" + strings.ToLower(path), URL: raw}, nil
	}
	if parsed != nil && parsed.Scheme == "file" {
		absolute := filepath.Clean(parsed.Path)
		return Repository{Identity: "file:" + filepath.ToSlash(absolute), URL: raw}, nil
	}
	if filepath.IsAbs(raw) {
		absolute := filepath.Clean(raw)
		return Repository{Identity: "file:" + filepath.ToSlash(absolute), URL: absolute}, nil
	}
	return Repository{}, &ValidationError{Problem: fmt.Sprintf("unsupported repository %q", raw)}
}

func (operation *GitOperation) repository(ctx context.Context, repository Repository) (*acquiredRepository, error) {
	if acquired := operation.acquired[repository.Identity]; acquired != nil {
		return acquired, nil
	}
	if err := os.MkdirAll(operation.cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create repository cache: %w", err)
	}
	if !operation.cleaned {
		entries, err := os.ReadDir(operation.cacheDir)
		if err != nil {
			return nil, fmt.Errorf("inspect repository cache: %w", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".repo-init-") || strings.Contains(entry.Name(), ".corrupt-") {
				if err := os.RemoveAll(filepath.Join(operation.cacheDir, entry.Name())); err != nil {
					return nil, fmt.Errorf("clean interrupted cache initialization %q: %w", entry.Name(), err)
				}
			}
		}
		operation.cleaned = true
	}
	key := sha256.Sum256([]byte(repository.Identity))
	dir := filepath.Join(operation.cacheDir, hex.EncodeToString(key[:]))
	if valid, _ := operation.validBareRepository(ctx, dir, repository.URL); !valid {
		if _, err := os.Lstat(dir); err == nil {
			quarantine := dir + ".corrupt-" + strconvTime()
			if err := os.Rename(dir, quarantine); err != nil {
				return nil, fmt.Errorf("quarantine invalid cache %q: %w", dir, err)
			}
			defer os.RemoveAll(quarantine)
		}
		temporary, err := os.MkdirTemp(operation.cacheDir, ".repo-init-")
		if err != nil {
			return nil, fmt.Errorf("create cache temporary directory: %w", err)
		}
		defer os.RemoveAll(temporary)
		if _, err := operation.run(ctx, "init", "--bare", temporary); err != nil {
			return nil, fmt.Errorf("initialize repository cache: %w", err)
		}
		if _, err := operation.run(ctx, "--git-dir="+temporary, "remote", "add", "origin", repository.URL); err != nil {
			return nil, fmt.Errorf("configure repository cache: %w", err)
		}
		if err := os.Rename(temporary, dir); err != nil {
			return nil, fmt.Errorf("publish repository cache: %w", err)
		}
	}
	acquired := &acquiredRepository{dir: dir}
	operation.acquired[repository.Identity] = acquired
	return acquired, nil
}

func (operation *GitOperation) validBareRepository(ctx context.Context, dir, remote string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false, err
	}
	bare, err := operation.run(ctx, "--git-dir="+dir, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(bare) != "true" {
		return false, err
	}
	origin, err := operation.run(ctx, "--git-dir="+dir, "remote", "get-url", "origin")
	return err == nil && strings.TrimSpace(origin) == remote, err
}

func (operation *GitOperation) fetchAll(ctx context.Context, repository *acquiredRepository) error {
	_, err := operation.run(ctx, "--git-dir="+repository.dir, "fetch", "--force", "--prune", "--no-recurse-submodules", "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*", "+HEAD:refs/remotes/origin/HEAD")
	if err == nil {
		repository.fetched = true
	}
	return err
}

func (operation *GitOperation) fetchExact(ctx context.Context, repository *acquiredRepository, commit string) error {
	_, err := operation.run(ctx, "--git-dir="+repository.dir, "fetch", "--force", "--no-tags", "--no-recurse-submodules", "origin", commit+":refs/skillsrc/pins/"+commit)
	if err != nil {
		return err
	}
	if !operation.hasCommit(ctx, repository.dir, commit) {
		return fmt.Errorf("remote did not provide commit %s", commit)
	}
	return nil
}

func (operation *GitOperation) resolveFetched(ctx context.Context, dir, ref string) (string, error) {
	if isCommitID(ref) {
		if !operation.hasCommit(ctx, dir, ref) {
			return "", fmt.Errorf("commit %s was not fetched", ref)
		}
		return strings.ToLower(ref), nil
	}
	if ref == "" {
		return operation.revParse(ctx, dir, "refs/remotes/origin/HEAD^{commit}")
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "~^:?*[\\") {
		return "", &ValidationError{Problem: fmt.Sprintf("invalid Git ref %q", ref)}
	}
	branch, branchErr := operation.revParse(ctx, dir, "refs/remotes/origin/"+ref+"^{commit}")
	tag, tagErr := operation.revParse(ctx, dir, "refs/tags/"+ref+"^{commit}")
	if branchErr == nil && tagErr == nil {
		return "", &ValidationError{Problem: fmt.Sprintf("ref %q is ambiguous between a branch and tag", ref)}
	}
	if branchErr == nil {
		return branch, nil
	}
	if tagErr == nil {
		return tag, nil
	}
	return "", fmt.Errorf("branch or tag %q was not found", ref)
}

func (operation *GitOperation) revParse(ctx context.Context, dir, revision string) (string, error) {
	output, err := operation.run(ctx, "--git-dir="+dir, "rev-parse", "--verify", revision)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(output)
	if err := validateCommitID(commit); err != nil {
		return "", err
	}
	return strings.ToLower(commit), nil
}

func (operation *GitOperation) hasCommit(ctx context.Context, dir, commit string) bool {
	_, err := operation.run(ctx, "--git-dir="+dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func (operation *GitOperation) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, operation.git, args...)
	command.Env = gitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", &GitError{Args: append([]string(nil), args...), Output: string(output), Err: err}
	}
	return string(output), nil
}

func gitEnvironment() []string {
	return append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
}

func (operation *GitOperation) lock(ctx context.Context) (func(), error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("skillsrc does not support Windows")
	}
	if err := os.MkdirAll(operation.cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	unlock, err := lockFileContext(ctx, filepath.Join(operation.cacheDir, ".lock"))
	if err != nil {
		return nil, fmt.Errorf("lock cache: %w", err)
	}
	return unlock, nil
}

func extractTar(ctx context.Context, reader io.Reader, destination string) error {
	archive := tar.NewReader(reader)
	seen := make(map[string]struct{})
	type archivedSymlink struct{ path, target string }
	var symlinks []archivedSymlink
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			for _, link := range symlinks {
				if err := os.MkdirAll(filepath.Dir(link.path), 0o755); err != nil {
					return fmt.Errorf("create archive symlink parent: %w", err)
				}
				if err := os.Symlink(link.target, link.path); err != nil {
					return fmt.Errorf("create archive symlink %q: %w", link.path, err)
				}
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Git archive: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return &ValidationError{Problem: fmt.Sprintf("Git archive contains escaping path %q", header.Name)}
		}
		if _, exists := seen[name]; exists {
			return &ValidationError{Problem: fmt.Sprintf("Git archive contains duplicate path %q", header.Name)}
		}
		seen[name] = struct{}{}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, os.FileMode(header.Mode)&0o755); err != nil {
				return fmt.Errorf("create archive directory %q: %w", path, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create archive parent: %w", err)
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o755)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", path, err)
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract archive file %q: %w", path, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close archive file %q: %w", path, closeErr)
			}
		case tar.TypeSymlink:
			symlinks = append(symlinks, archivedSymlink{path: path, target: header.Linkname})
		default:
			return &ValidationError{Problem: fmt.Sprintf("Git archive path %q has unsupported type", header.Name)}
		}
	}
}

func isCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateCommitID(value string) error {
	if !isCommitID(value) {
		return &ValidationError{Problem: fmt.Sprintf("invalid exact commit %q", value)}
	}
	return nil
}

func strconvTime() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }
