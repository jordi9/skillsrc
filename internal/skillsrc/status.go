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
	Name          string `json:"name"`
	Source        string `json:"source"`
	ConfiguredRef string `json:"configured_ref,omitempty"`
	LockedCommit  string `json:"locked_commit,omitempty"`
	LockedHash    string `json:"locked_hash,omitempty"`
	ResolvedPath  string `json:"resolved_path,omitempty"`
	Status        string `json:"status"`
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
	manifest, lock, err := engine.load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	installer := newInstaller(engine.options.TargetDir, manifest.Path)
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
		for _, name := range source.Skills {
			status := SkillStatus{Name: name, Source: sourceDisplay, ConfiguredRef: source.Ref, Status: "unlocked"}
			locked := findLockedSkill(lock, kind, identity, source, name)
			if locked != nil {
				status.LockedCommit = locked.source.Commit
				status.LockedHash = locked.skill.Hash
				status.ResolvedPath = locked.skill.Path
				status.Status = installedStatus(installer, engine.options.TargetDir, identity, *locked.skill)
			}
			statuses = append(statuses, status)
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
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
		if kind == SourceGit && (source.Repo != manifestSource.Repo || source.Ref != manifestSource.Ref) {
			continue
		}
		if kind == SourceLocal && source.Path != manifestSource.Path {
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

func installedStatus(installer *installer, target, identity string, skill LockedSkill) string {
	dir := filepath.Join(target, skill.Name)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "collision"
	}
	data, err := os.ReadFile(filepath.Join(dir, ownershipFile))
	if err != nil {
		return "collision"
	}
	var marker ownership
	if json.Unmarshal(data, &marker) != nil || marker.Version != SchemaVersion || marker.Owner != installer.owner || marker.Skill != skill.Name {
		return "collision"
	}
	hash, err := HashSkill(dir)
	if err != nil || hash != skill.Hash || marker.Hash != skill.Hash || marker.Source != identity {
		return "drifted"
	}
	return "current"
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
	report := DoctorReport{}
	declared := make(map[string]struct{})
	for _, source := range manifest.Sources {
		for _, name := range source.Skills {
			declared[name] = struct{}{}
		}
	}
	for _, status := range statuses {
		switch status.Status {
		case "unlocked":
			report.Issues = append(report.Issues, DoctorIssue{Kind: "lock", Skill: status.Name, Message: "declaration has no consistent lock entry"})
		case "missing", "drifted", "collision":
			report.Issues = append(report.Issues, DoctorIssue{Kind: "install", Skill: status.Name, Message: "managed install is " + status.Status})
		}
	}
	for _, source := range lock.Sources {
		for _, skill := range source.Skills {
			if _, ok := declared[skill.Name]; !ok {
				report.Issues = append(report.Issues, DoctorIssue{Kind: "lock", Skill: skill.Name, Message: "lock entry is not declared by the manifest"})
			}
		}
		if source.Kind == SourceGit {
			dir := repositoryCachePath(engine.options.CacheDir, source.Identity)
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				report.Issues = append(report.Issues, DoctorIssue{Kind: "cache", Message: fmt.Sprintf("cache is missing for %s", source.Identity)})
				continue
			}
			operation := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
			if !operation.hasCommit(ctx, dir, source.Commit) {
				report.Issues = append(report.Issues, DoctorIssue{Kind: "cache", Message: fmt.Sprintf("cache for %s lacks commit %s", source.Identity, source.Commit)})
			}
		}
	}
	if entries, err := os.ReadDir(engine.options.CacheDir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".repo-init-") || strings.Contains(entry.Name(), ".corrupt-") {
				report.Issues = append(report.Issues, DoctorIssue{Kind: "cache", Message: "interrupted cache artifact " + entry.Name()})
			}
		}
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
