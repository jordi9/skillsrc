package skillsrc

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Options struct {
	ManifestPath string
	LockPath     string
	TargetDir    string
	CacheDir     string
	GitBinary    string
	Out          io.Writer
	Err          io.Writer
}

type Result struct {
	Acquisitions int      `json:"acquisitions"`
	Changes      []Change `json:"changes,omitempty"`
	LocalSkipped []string `json:"local_skipped,omitempty"`
}

type Change struct {
	Source string `json:"source"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new"`
}

type Engine struct{ options Options }

func NewEngine(options Options) *Engine { return &Engine{options: options} }

type resolvedSource struct {
	lock LockSource
	root string
}

func (engine *Engine) Sync(ctx context.Context) (Result, error) {
	manifest, oldLock, err := engine.load()
	if err != nil {
		return Result{}, err
	}
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	resolved, err := engine.resolve(ctx, manifest, oldLock, git, nil, false)
	if err != nil {
		return Result{Acquisitions: git.Acquisitions()}, err
	}
	newLock := lockFromResolved(resolved)
	if err := engine.apply(ctx, manifest, oldLock, newLock, resolved); err != nil {
		return Result{Acquisitions: git.Acquisitions()}, err
	}
	return Result{Acquisitions: git.Acquisitions()}, nil
}

func (engine *Engine) Update(ctx context.Context, selectors []string) (Result, error) {
	manifest, oldLock, err := engine.load()
	if err != nil {
		return Result{}, err
	}
	selected, err := selectUpdates(manifest, selectors)
	if err != nil {
		return Result{}, err
	}
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	resolved, err := engine.resolve(ctx, manifest, oldLock, git, selected, true)
	if err != nil {
		return Result{Acquisitions: git.Acquisitions()}, err
	}
	newLock := lockFromResolved(resolved)
	changes := lockChanges(oldLock, newLock, selected)
	locals := selectedLocalSources(manifest, selected)
	if err := engine.apply(ctx, manifest, oldLock, newLock, resolved); err != nil {
		return Result{Acquisitions: git.Acquisitions(), Changes: changes, LocalSkipped: locals}, err
	}
	return Result{Acquisitions: git.Acquisitions(), Changes: changes, LocalSkipped: locals}, nil
}

func (engine *Engine) load() (Manifest, Lock, error) {
	manifest, err := LoadManifest(engine.options.ManifestPath)
	if err != nil {
		return Manifest{}, Lock{}, err
	}
	lock, err := LoadLock(engine.options.LockPath)
	if err != nil {
		return Manifest{}, Lock{}, err
	}
	return manifest, lock, nil
}

func (engine *Engine) resolve(ctx context.Context, manifest Manifest, oldLock Lock, git *GitOperation, selected map[int]bool, updating bool) ([]resolvedSource, error) {
	resolved := make([]resolvedSource, 0, len(manifest.Sources))
	for index, source := range manifest.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source.Path != "" {
			entry, err := resolveLocalSource(source)
			if err != nil {
				return nil, fmt.Errorf("local source %q: %w", source.Path, err)
			}
			resolved = append(resolved, entry)
			continue
		}

		repository, err := NormalizeRepository(source.Repo)
		if err != nil {
			return nil, err
		}
		previous := matchingLockSource(oldLock, source, repository.Identity)
		refresh := previous == nil
		lockedCommit := ""
		if previous != nil {
			lockedCommit = previous.Commit
		}
		if updating && selected[index] && !isCommitID(source.Ref) {
			refresh = true
		}
		commit, err := git.Resolve(ctx, source.Repo, source.Ref, refresh, lockedCommit)
		if err != nil {
			return nil, err
		}
		root, err := os.MkdirTemp("", "skillsrc-git-")
		if err != nil {
			return nil, err
		}
		if err := git.Materialize(ctx, source.Repo, commit, root); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		entry, err := resolveDiscoveredSource(root, LockSource{Kind: SourceGit, Identity: repository.Identity, Repo: source.Repo, Ref: source.Ref, Commit: commit}, source.Skills)
		if err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("Git source %q at %s: %w", source.Repo, commit, err)
		}
		entry.root = root
		resolved = append(resolved, entry)
	}
	return resolved, nil
}

func resolveLocalSource(source ManifestSource) (resolvedSource, error) {
	identityPath := source.Path
	if !filepath.IsAbs(identityPath) && !strings.HasPrefix(identityPath, "~/") && identityPath != "~" {
		identityPath = filepath.ToSlash(filepath.Clean(identityPath))
	}
	entry := LockSource{Kind: SourceLocal, Identity: LocalIdentity(identityPath), Path: source.Path}
	return resolveDiscoveredSource(source.ResolvedPath, entry, source.Skills)
}

func resolveDiscoveredSource(root string, lockSource LockSource, selected []string) (resolvedSource, error) {
	found, err := DiscoverSkills(root)
	if err != nil {
		return resolvedSource{}, err
	}
	for _, name := range selected {
		discovered, ok := found[name]
		if !ok {
			return resolvedSource{}, &ValidationError{Problem: fmt.Sprintf("selected skill %q was not found", name)}
		}
		sourceDir := root
		if discovered.Path != "." {
			if err := ValidateRelativeSkillPath(discovered.Path); err != nil {
				return resolvedSource{}, err
			}
			sourceDir = filepath.Join(root, filepath.FromSlash(discovered.Path))
		}
		hash, err := HashSkill(sourceDir)
		if err != nil {
			return resolvedSource{}, err
		}
		lockSource.Skills = append(lockSource.Skills, LockedSkill{Name: name, Path: discovered.Path, Hash: hash})
	}
	return resolvedSource{lock: lockSource, root: root}, nil
}

func (engine *Engine) apply(ctx context.Context, manifest Manifest, oldLock, newLock Lock, resolved []resolvedSource) error {
	defer func() {
		for _, source := range resolved {
			if source.lock.Kind == SourceGit {
				_ = os.RemoveAll(source.root)
			}
		}
	}()
	installer := newInstaller(engine.options.TargetDir, manifest.Path)
	return installer.withLock(ctx, func() error {
		for _, source := range resolved {
			for _, skill := range source.lock.Skills {
				dir := source.root
				if skill.Path != "." {
					dir = filepath.Join(source.root, filepath.FromSlash(skill.Path))
				}
				if err := installer.install(ctx, skill.Name, source.lock.Identity, dir, skill.Hash); err != nil {
					return fmt.Errorf("install %q: %w", skill.Name, err)
				}
			}
		}
		desired := lockSkillNames(newLock)
		for name := range lockSkillNames(oldLock) {
			if _, keep := desired[name]; !keep {
				if err := installer.prune(name); err != nil {
					return err
				}
			}
		}
		encoded, err := EncodeLock(newLock)
		if err != nil {
			return err
		}
		if err := writeAtomic(engine.options.LockPath, encoded, 0o644); err != nil {
			return fmt.Errorf("write lockfile: %w", err)
		}
		return nil
	})
}

func matchingLockSource(lock Lock, source ManifestSource, identity string) *LockSource {
	wanted := append([]string(nil), source.Skills...)
	sort.Strings(wanted)
	for i := range lock.Sources {
		candidate := &lock.Sources[i]
		if candidate.Kind != SourceGit || candidate.Identity != identity || candidate.Repo != source.Repo || candidate.Ref != source.Ref {
			continue
		}
		got := make([]string, len(candidate.Skills))
		for j, skill := range candidate.Skills {
			got[j] = skill.Name
		}
		sort.Strings(got)
		if strings.Join(got, "\x00") == strings.Join(wanted, "\x00") {
			return candidate
		}
	}
	return nil
}

func lockFromResolved(resolved []resolvedSource) Lock {
	lock := Lock{Version: SchemaVersion, Sources: make([]LockSource, len(resolved))}
	for i := range resolved {
		lock.Sources[i] = resolved[i].lock
	}
	return lock
}

func lockSkillNames(lock Lock) map[string]struct{} {
	names := make(map[string]struct{})
	for _, source := range lock.Sources {
		for _, skill := range source.Skills {
			names[skill.Name] = struct{}{}
		}
	}
	return names
}

func selectUpdates(manifest Manifest, selectors []string) (map[int]bool, error) {
	selected := make(map[int]bool)
	if len(selectors) == 0 {
		for i := range manifest.Sources {
			selected[i] = true
		}
		return selected, nil
	}
	matched := make(map[string]bool)
	for i, source := range manifest.Sources {
		identity := source.Path
		if source.Repo != "" {
			identity = source.Repo
		}
		for _, selector := range selectors {
			if selector == identity || contains(source.Skills, selector) {
				selected[i] = true
				matched[selector] = true
			}
		}
	}
	for _, selector := range selectors {
		if !matched[selector] {
			return nil, &ValidationError{Problem: fmt.Sprintf("update selector %q matches no source or skill", selector)}
		}
	}
	return selected, nil
}

func selectedLocalSources(manifest Manifest, selected map[int]bool) []string {
	var local []string
	for index, source := range manifest.Sources {
		if selected[index] && source.Path != "" {
			local = append(local, source.Path)
		}
	}
	sort.Strings(local)
	return local
}

func lockChanges(oldLock, newLock Lock, selected map[int]bool) []Change {
	var changes []Change
	for index, source := range newLock.Sources {
		if !selected[index] || source.Kind != SourceGit {
			continue
		}
		old := ""
		for _, previous := range oldLock.Sources {
			if previous.Kind == SourceGit && previous.Identity == source.Identity && previous.Ref == source.Ref {
				old = previous.Commit
				break
			}
		}
		if old != source.Commit {
			changes = append(changes, Change{Source: source.Repo, Old: old, New: source.Commit})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Source < changes[j].Source })
	return changes
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func DefaultOptions() (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Options{}, err
	}
	manifest := filepath.Join(home, ".agents", ".skillsrc")
	return Options{
		ManifestPath: manifest,
		LockPath:     filepath.Join(filepath.Dir(manifest), "skills.lock"),
		TargetDir:    filepath.Join(home, ".agents", "skills"),
		CacheDir:     filepath.Join(cache, "skillsrc", "repos"),
		GitBinary:    "git",
		Out:          os.Stdout,
		Err:          os.Stderr,
	}, nil
}
