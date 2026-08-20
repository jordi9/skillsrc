package skillsrc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SkillStatus struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	ConfiguredRef   string `json:"configured_ref,omitempty"`
	LockedCommit    string `json:"locked_commit,omitempty"`
	LockedHash      string `json:"locked_hash,omitempty"`
	ResolvedPath    string `json:"resolved_path,omitempty"`
	ModelInvocation string `json:"model_invocation"`
	Status          string `json:"status"`
}

type DoctorReport struct {
	Issues []DoctorIssue `json:"issues"`
}

type DoctorIssue struct {
	Kind    string `json:"kind"`
	Skill   string `json:"skill,omitempty"`
	Message string `json:"message"`
}

func (engine *Engine) List(ctx context.Context) ([]SkillStatus, error) {
	return engine.list(ctx, false)
}

func (engine *Engine) ListAll(ctx context.Context) ([]SkillStatus, error) {
	return engine.list(ctx, true)
}

func (engine *Engine) list(ctx context.Context, includeUnmanaged bool) ([]SkillStatus, error) {
	manifest, lock, err := engine.load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	installer := engine.newInstaller()
	statuses := make([]SkillStatus, 0)
	for _, source := range manifest.Sources {
		sourceDisplay := source.Path
		kind := SourceLocal
		identity := LocalIdentity(localIdentityPath(source.Path))
		if source.Repo != "" {
			sourceDisplay = source.Repo
			kind = SourceGit
			repository, err := NormalizeRepository(source.Repo)
			if err != nil {
				return nil, err
			}
			identity = repository.Identity
		}
		names := append([]string(nil), source.Skills...)
		for _, name := range names {
			status := SkillStatus{
				Name:            name,
				Source:          sourceDisplay,
				ConfiguredRef:   source.Ref,
				ModelInvocation: "enabled",
				Status:          "unlocked",
			}
			locked := findLockedSkill(lock, kind, identity, source, name)
			if locked != nil {
				status.LockedCommit = locked.source.Commit
				status.LockedHash = locked.skill.Hash
				status.ResolvedPath = locked.skill.Path
				status.Status = installedStatus(installer, engine.options.TargetDir, *locked.skill, source.DisableModelInvocation[name])
				if locked.skill.SourceDisablesModelInvocation {
					status.ModelInvocation = "disabled by source"
				} else if source.DisableModelInvocation[name] {
					status.ModelInvocation = "disabled by config"
				}
			} else if source.DisableModelInvocation[name] {
				status.ModelInvocation = "disabled by config"
			}
			statuses = append(statuses, status)
		}
	}
	if includeUnmanaged {
		statuses, err = engine.appendUnmanagedStatuses(ctx, installer, statuses)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

func (engine *Engine) appendUnmanagedStatuses(ctx context.Context, installer *installer, statuses []SkillStatus) ([]SkillStatus, error) {
	entries, err := os.ReadDir(engine.options.TargetDir)
	if errors.Is(err, os.ErrNotExist) {
		return statuses, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan install target %q: %w", engine.options.TargetDir, err)
	}
	represented := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		represented[status.Name] = struct{}{}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(engine.options.TargetDir, entry.Name())
		if _, exists := represented[entry.Name()]; exists {
			continue
		}
		managed, _ := installer.managed(dir, entry.Name())
		if managed {
			continue
		}
		if !isRegularFile(filepath.Join(dir, "SKILL.md")) {
			continue
		}
		if _, err := HashSkill(dir); err != nil {
			return nil, fmt.Errorf("validate unmanaged skill %q: %w", dir, err)
		}
		name, err := skillName(dir)
		if err != nil {
			return nil, err
		}
		if _, exists := represented[name]; exists {
			continue
		}
		disabled, err := sourceDisablesModelInvocation(dir)
		if err != nil {
			return nil, err
		}
		invocation := "enabled"
		if disabled {
			invocation = "disabled by source"
		}
		statuses = append(statuses, SkillStatus{
			Name:            name,
			Source:          "unmanaged",
			ModelInvocation: invocation,
			Status:          "unmanaged",
		})
		represented[name] = struct{}{}
	}
	return statuses, nil
}

type lockedSkillMatch struct {
	source *LockSource
	skill  *LockedSkill
}

func findLockedSkill(lock Lock, kind SourceKind, identity string, manifestSource ManifestSource, name string) *lockedSkillMatch {
	for i := range lock.Sources {
		source := &lock.Sources[i]
		if source.Kind != kind || source.Identity != identity {
			continue
		}
		if !lockSourceMatchesManifest(*source, kind, identity, manifestSource) {
			continue
		}
		for j := range source.Skills {
			if source.Skills[j].Name == name {
				return &lockedSkillMatch{source: source, skill: &source.Skills[j]}
			}
		}
	}
	return nil
}

func lockSourceMatchesManifest(source LockSource, kind SourceKind, identity string, manifestSource ManifestSource) bool {
	if source.Kind != kind || source.Identity != identity {
		return false
	}
	if kind == SourceGit {
		return source.Repo == manifestSource.Repo && source.Ref == manifestSource.Ref
	}
	return source.Path == manifestSource.Path
}

func installedStatus(installer *installer, target string, skill LockedSkill, disableModelInvocation bool) string {
	dir := filepath.Join(target, skill.Name)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "collision"
	}
	marker, ok := installedMarker(installer, dir, skill.Name)
	if !ok {
		return "collision"
	}
	if marker.DisableModelInvocation != disableModelInvocation {
		return "drifted"
	}
	expectedHash := marker.InstalledHash
	if marker.SourceHash == "" && marker.InstalledHash == "" && !disableModelInvocation {
		// Markers created before derived installs were introduced remain valid.
		expectedHash = skill.Hash
	} else if marker.SourceHash != skill.Hash {
		return "drifted"
	}
	hash, err := HashSkill(dir)
	if err != nil || expectedHash == "" || hash != expectedHash {
		return "drifted"
	}
	return "current"
}

func installedMarker(installer *installer, dir, skill string) (ownership, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ownershipFile))
	if err != nil {
		return ownership{}, false
	}
	var marker ownership
	if json.Unmarshal(data, &marker) != nil || marker.Version != SchemaVersion || marker.Owner != installer.owner || marker.Skill != skill {
		return ownership{}, false
	}
	return marker, true
}

func (engine *Engine) Doctor(ctx context.Context, repair bool) (DoctorReport, error) {
	if repair {
		if _, err := engine.Sync(ctx); err != nil {
			return DoctorReport{}, fmt.Errorf("repair: %w", err)
		}
	}
	manifest, lock, err := engine.load()
	if err != nil {
		return DoctorReport{}, err
	}
	statuses, err := engine.List(ctx)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{Issues: []DoctorIssue{}}
	report.Issues = append(report.Issues, localSourceIssues(manifest, lock)...)
	for _, status := range statuses {
		switch status.Status {
		case "unlocked":
			report.Issues = append(report.Issues, DoctorIssue{Kind: "lock", Skill: status.Name, Message: "declaration has no consistent lock entry"})
		case "missing", "drifted", "collision":
			report.Issues = append(report.Issues, DoctorIssue{Kind: "install", Skill: status.Name, Message: "managed install is " + status.Status})
		}
	}
	report.Issues = append(report.Issues, engine.lockAndCacheIssues(ctx, manifest, lock)...)
	report.Issues = append(report.Issues, interruptedArtifactIssues(engine.options.CacheDir, engine.options.TargetDir)...)
	if engine.options.ProjectRoot != "" {
		projectIssues, err := CheckProjectFiles(engine.options.ProjectRoot, lock)
		if err != nil {
			return DoctorReport{}, err
		}
		report.Issues = append(report.Issues, projectIssues...)
	}
	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Kind != report.Issues[j].Kind {
			return report.Issues[i].Kind < report.Issues[j].Kind
		}
		if report.Issues[i].Skill != report.Issues[j].Skill {
			return report.Issues[i].Skill < report.Issues[j].Skill
		}
		return report.Issues[i].Message < report.Issues[j].Message
	})
	return report, nil
}

func localSourceIssues(manifest Manifest, lock Lock) []DoctorIssue {
	var issues []DoctorIssue
	for _, source := range manifest.Sources {
		if source.Path == "" {
			continue
		}
		current, err := resolveLocalSource(source)
		if err != nil {
			issues = append(issues, DoctorIssue{Kind: "source", Message: fmt.Sprintf("local source %s is invalid: %v", source.Path, err)})
			continue
		}
		identity := LocalIdentity(localIdentityPath(source.Path))
		for _, skill := range current.lock.Skills {
			locked := findLockedSkill(lock, SourceLocal, identity, source, skill.Name)
			if locked != nil && (locked.skill.Path != skill.Path || locked.skill.Hash != skill.Hash) {
				issues = append(issues, DoctorIssue{Kind: "source", Skill: skill.Name, Message: "local source content differs from lock"})
			}
		}
	}
	return issues
}

func (engine *Engine) lockAndCacheIssues(ctx context.Context, manifest Manifest, lock Lock) []DoctorIssue {
	declared := make(map[string]struct{})
	for _, source := range manifest.Sources {
		for _, name := range source.Skills {
			declared[name] = struct{}{}
		}
	}
	var issues []DoctorIssue
	for _, source := range lock.Sources {
		for _, skill := range source.Skills {
			if _, ok := declared[skill.Name]; !ok {
				issues = append(issues, DoctorIssue{Kind: "lock", Skill: skill.Name, Message: "lock entry is not declared by the manifest"})
			}
		}
		if source.Kind != SourceGit {
			continue
		}
		dir := repositoryCachePath(engine.options.CacheDir, source.Identity)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			issues = append(issues, DoctorIssue{Kind: "cache", Message: fmt.Sprintf("cache is missing for %s", source.Identity)})
			continue
		}
		operation := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
		repository, normalizeErr := NormalizeRepository(source.Repo)
		valid := false
		if normalizeErr == nil {
			valid, _ = operation.validBareRepository(ctx, dir, repository.URL)
		}
		if !valid {
			issues = append(issues, DoctorIssue{Kind: "cache", Message: fmt.Sprintf("cache is invalid for %s", source.Identity)})
			continue
		}
		if !operation.hasCommit(ctx, dir, source.Commit) {
			issues = append(issues, DoctorIssue{Kind: "cache", Message: fmt.Sprintf("cache for %s lacks commit %s", source.Identity, source.Commit)})
		}
	}
	return issues
}

func interruptedArtifactIssues(cacheDir, targetDir string) []DoctorIssue {
	var issues []DoctorIssue
	if entries, err := os.ReadDir(cacheDir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".repo-init-") || strings.Contains(entry.Name(), ".corrupt-") {
				issues = append(issues, DoctorIssue{Kind: "cache", Message: "interrupted cache artifact " + entry.Name()})
			}
		}
	}
	if entries, err := os.ReadDir(targetDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.Contains(name, ".skillsrc-tmp-") || strings.Contains(name, ".skillsrc-old-") || strings.Contains(name, ".skillsrc-prune-") || strings.HasSuffix(name, ".skillsrc-txn.json") {
				issues = append(issues, DoctorIssue{Kind: "install", Message: "interrupted install artifact " + name})
			}
		}
	}
	return issues
}

func localIdentityPath(path string) string {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "~/") || path == "~" {
		return path
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func repositoryCachePath(cacheDir, identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(cacheDir, hex.EncodeToString(digest[:]))
}
