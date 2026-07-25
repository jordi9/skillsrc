package skillsrc

import (
	"context"
	"errors"
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
	Fetches      []FetchEvent  `json:"fetches,omitempty"`
	Skills       []SkillAction `json:"skills,omitempty"`
	Changes      []Change      `json:"changes,omitempty"`
	LocalSkipped []string      `json:"local_skipped,omitempty"`
}

type FetchEvent struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
	Commit string `json:"commit,omitempty"`
}

type SkillAction struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

type Change struct {
	Source string `json:"source"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new"`
}

type Engine struct{ options Options }

func NewEngine(options Options) *Engine { return &Engine{options: options} }

func (engine *Engine) Add(ctx context.Context, source ManifestSource, requested []string, all bool) ([]string, Result, error) {
	var available []string
	var result Result
	installer := newInstaller(engine.options.TargetDir, engine.options.ManifestPath)
	err := installer.withLock(ctx, func() error {
		var operationErr error
		available, result, operationErr = engine.addLocked(ctx, installer, source, requested, all)
		return operationErr
	})
	return available, result, err
}

func (engine *Engine) addLocked(ctx context.Context, installer *installer, source ManifestSource, requested []string, all bool) ([]string, Result, error) {
	manifest, err := LoadManifest(engine.options.ManifestPath)
	if errors.Is(err, os.ErrNotExist) {
		absolute, pathErr := filepath.Abs(engine.options.ManifestPath)
		if pathErr != nil {
			return nil, Result{}, pathErr
		}
		manifest = Manifest{Version: SchemaVersion, Path: filepath.Clean(absolute)}
	} else if err != nil {
		return nil, Result{}, err
	}
	oldLock, err := LoadLock(engine.options.LockPath)
	if err != nil {
		return nil, Result{}, err
	}
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	available, err := engine.discover(ctx, source, git)
	result := Result{Fetches: git.Fetches()}
	if err != nil {
		return nil, result, err
	}
	selected := append([]string(nil), requested...)
	if all {
		selected = append([]string(nil), available...)
	}
	found := make(map[string]struct{}, len(available))
	for _, name := range available {
		found[name] = struct{}{}
	}
	for _, name := range selected {
		if _, ok := found[name]; !ok {
			return available, result, &ValidationError{Problem: fmt.Sprintf("selected skill %q was not found", name)}
		}
	}
	index, err := manifestSourceIndex(manifest, source)
	if err != nil {
		return available, result, err
	}
	already := make(map[string]int)
	for sourceIndex, declared := range manifest.Sources {
		for _, name := range declared.Skills {
			already[name] = sourceIndex
		}
	}
	if index < 0 {
		index = len(manifest.Sources)
		source.Skills = nil
		source.ResolvedPath = ""
		manifest.Sources = append(manifest.Sources, source)
	}
	selectedSet := make(map[string]struct{}, len(manifest.Sources[index].Skills))
	for _, name := range manifest.Sources[index].Skills {
		selectedSet[name] = struct{}{}
	}
	for _, name := range selected {
		if owner, exists := already[name]; exists && owner != index {
			return available, result, &ValidationError{Problem: fmt.Sprintf("skill %q is already declared by another source", name)}
		}
		if _, exists := selectedSet[name]; !exists {
			manifest.Sources[index].Skills = append(manifest.Sources[index].Skills, name)
			selectedSet[name] = struct{}{}
		}
	}
	if err := validateManifest(&manifest); err != nil {
		return available, result, err
	}
	resolved, err := engine.resolve(ctx, manifest, oldLock, git, nil, false)
	result.Fetches = git.Fetches()
	if err != nil {
		return available, result, err
	}
	newLock := lockFromResolved(resolved)
	if err := writeManifest(engine.options.ManifestPath, manifest); err != nil {
		for _, entry := range resolved {
			if entry.lock.Kind == SourceGit {
				_ = os.RemoveAll(entry.root)
			}
		}
		return available, result, err
	}
	actions, err := engine.applyLocked(ctx, installer, oldLock, newLock, resolved)
	result.Skills = actions
	if err != nil {
		return available, result, fmt.Errorf("manifest updated; sync incomplete: %w", err)
	}
	return available, result, nil
}

func (engine *Engine) Remove(ctx context.Context, names []string) (Result, error) {
	var result Result
	installer := newInstaller(engine.options.TargetDir, engine.options.ManifestPath)
	err := installer.withLock(ctx, func() error {
		var operationErr error
		result, operationErr = engine.removeLocked(ctx, installer, names)
		return operationErr
	})
	return result, err
}

func (engine *Engine) removeLocked(ctx context.Context, installer *installer, names []string) (Result, error) {
	manifest, oldLock, err := engine.load()
	if err != nil {
		return Result{}, err
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	declared := make(map[string]struct{})
	for _, source := range manifest.Sources {
		for _, name := range source.Skills {
			declared[name] = struct{}{}
		}
	}
	for _, name := range names {
		if _, ok := declared[name]; !ok {
			return Result{}, &ValidationError{Problem: fmt.Sprintf("skill %q is not declared", name)}
		}
	}
	keptSources := manifest.Sources[:0]
	for _, source := range manifest.Sources {
		keptSkills := source.Skills[:0]
		for _, name := range source.Skills {
			if _, remove := wanted[name]; !remove {
				keptSkills = append(keptSkills, name)
			}
		}
		if len(keptSkills) > 0 {
			source.Skills = keptSkills
			keptSources = append(keptSources, source)
		}
	}
	manifest.Sources = keptSources
	filteredLock := lockForManifest(oldLock, manifest)
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	resolved, err := engine.resolve(ctx, manifest, filteredLock, git, nil, false)
	result := Result{Fetches: git.Fetches()}
	if err != nil {
		return result, err
	}
	newLock := lockFromResolved(resolved)
	if err := writeManifest(engine.options.ManifestPath, manifest); err != nil {
		for _, entry := range resolved {
			if entry.lock.Kind == SourceGit {
				_ = os.RemoveAll(entry.root)
			}
		}
		return result, err
	}
	actions, err := engine.applyLocked(ctx, installer, oldLock, newLock, resolved)
	result.Skills = actions
	if err != nil {
		return result, fmt.Errorf("manifest updated; sync incomplete: %w", err)
	}
	return result, nil
}

func lockForManifest(lock Lock, manifest Manifest) Lock {
	filtered := Lock{Version: SchemaVersion}
	for _, source := range manifest.Sources {
		selected := make(map[string]struct{}, len(source.Skills))
		for _, name := range source.Skills {
			selected[name] = struct{}{}
		}
		for _, locked := range lock.Sources {
			matches := locked.Ref == source.Ref
			if source.Repo != "" {
				repository, err := NormalizeRepository(source.Repo)
				matches = matches && err == nil && locked.Kind == SourceGit && locked.Identity == repository.Identity
			} else {
				matches = matches && locked.Kind == SourceLocal && locked.Path == source.Path
			}
			if !matches {
				continue
			}
			kept := locked
			kept.Skills = nil
			for _, skill := range locked.Skills {
				if _, ok := selected[skill.Name]; ok {
					kept.Skills = append(kept.Skills, skill)
				}
			}
			if len(kept.Skills) == len(selected) {
				filtered.Sources = append(filtered.Sources, kept)
			}
			break
		}
	}
	return filtered
}

func writeManifest(path string, manifest Manifest) error {
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func manifestSourceIndex(manifest Manifest, candidate ManifestSource) (int, error) {
	if candidate.Repo != "" {
		wanted, err := NormalizeRepository(candidate.Repo)
		if err != nil {
			return -1, err
		}
		for index, source := range manifest.Sources {
			if source.Repo == "" || source.Ref != candidate.Ref {
				continue
			}
			repository, err := NormalizeRepository(source.Repo)
			if err != nil {
				return -1, err
			}
			if repository.Identity == wanted.Identity {
				return index, nil
			}
		}
		return -1, nil
	}
	for index, source := range manifest.Sources {
		if source.Path != "" && filepath.Clean(source.ResolvedPath) == filepath.Clean(candidate.ResolvedPath) {
			return index, nil
		}
	}
	return -1, nil
}

func (engine *Engine) Discover(ctx context.Context, source ManifestSource) ([]string, Result, error) {
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	names, err := engine.discover(ctx, source, git)
	return names, Result{Fetches: git.Fetches()}, err
}

func (engine *Engine) discover(ctx context.Context, source ManifestSource, git *GitOperation) ([]string, error) {
	root := source.ResolvedPath
	if source.Repo != "" {
		commit, err := git.Resolve(ctx, source.Repo, source.Ref, true, "")
		if err != nil {
			return nil, err
		}
		root, err = os.MkdirTemp("", "skillsrc-discover-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(root)
		if err := git.Materialize(ctx, source.Repo, commit, root); err != nil {
			return nil, err
		}
	}
	found, err := DiscoverSkills(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

type resolvedSource struct {
	lock LockSource
	root string
}

func (engine *Engine) Sync(ctx context.Context) (Result, error) {
	var result Result
	installer := newInstaller(engine.options.TargetDir, engine.options.ManifestPath)
	err := installer.withLock(ctx, func() error {
		var operationErr error
		result, operationErr = engine.syncLocked(ctx, installer)
		return operationErr
	})
	return result, err
}

func (engine *Engine) syncLocked(ctx context.Context, installer *installer) (Result, error) {
	manifest, oldLock, err := engine.load()
	if err != nil {
		return Result{}, err
	}
	git := NewGitOperation(engine.options.CacheDir, engine.options.GitBinary)
	resolved, err := engine.resolve(ctx, manifest, oldLock, git, nil, false)
	if err != nil {
		return Result{Fetches: git.Fetches()}, err
	}
	newLock := lockFromResolved(resolved)
	actions, err := engine.applyLocked(ctx, installer, oldLock, newLock, resolved)
	if err != nil {
		return Result{Fetches: git.Fetches(), Skills: actions}, err
	}
	return Result{Fetches: git.Fetches(), Skills: actions}, nil
}

func (engine *Engine) Update(ctx context.Context, selectors []string) (Result, error) {
	var result Result
	installer := newInstaller(engine.options.TargetDir, engine.options.ManifestPath)
	err := installer.withLock(ctx, func() error {
		var operationErr error
		result, operationErr = engine.updateLocked(ctx, installer, selectors)
		return operationErr
	})
	return result, err
}

func (engine *Engine) updateLocked(ctx context.Context, installer *installer, selectors []string) (Result, error) {
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
		return Result{Fetches: git.Fetches()}, err
	}
	newLock := lockFromResolved(resolved)
	changes := lockChanges(oldLock, newLock, selected)
	locals := selectedLocalSources(manifest, selected)
	actions, err := engine.applyLocked(ctx, installer, oldLock, newLock, resolved)
	if err != nil {
		return Result{Fetches: git.Fetches(), Skills: actions, Changes: changes, LocalSkipped: locals}, err
	}
	return Result{Fetches: git.Fetches(), Skills: actions, Changes: changes, LocalSkipped: locals}, nil
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

func (engine *Engine) applyLocked(ctx context.Context, installer *installer, oldLock, newLock Lock, resolved []resolvedSource) ([]SkillAction, error) {
	defer func() {
		for _, source := range resolved {
			if source.lock.Kind == SourceGit {
				_ = os.RemoveAll(source.root)
			}
		}
	}()
	var actions []SkillAction
	for _, source := range resolved {
		for _, skill := range source.lock.Skills {
			dir := source.root
			if skill.Path != "." {
				dir = filepath.Join(source.root, filepath.FromSlash(skill.Path))
			}
			state := installedStatus(installer, engine.options.TargetDir, source.lock.Identity, skill)
			if err := installer.install(ctx, skill.Name, source.lock.Identity, dir, skill.Hash); err != nil {
				return actions, fmt.Errorf("install %q: %w", skill.Name, err)
			}
			action := "repaired"
			if state == "missing" {
				action = "installed"
			} else if state == "current" {
				action = "unchanged"
			}
			actions = append(actions, SkillAction{Name: skill.Name, Action: action})
		}
	}
	desired := lockSkillNames(newLock)
	var oldNames []string
	for name := range lockSkillNames(oldLock) {
		oldNames = append(oldNames, name)
	}
	sort.Strings(oldNames)
	for _, name := range oldNames {
		if _, keep := desired[name]; keep {
			continue
		}
		_, statErr := os.Lstat(filepath.Join(engine.options.TargetDir, name))
		if err := installer.prune(name); err != nil {
			return actions, err
		}
		if statErr == nil {
			actions = append(actions, SkillAction{Name: name, Action: "pruned"})
		}
	}
	encoded, err := EncodeLock(newLock)
	if err != nil {
		return actions, err
	}
	if err := writeAtomic(engine.options.LockPath, encoded, 0o644); err != nil {
		return actions, fmt.Errorf("write lockfile: %w", err)
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Action != actions[j].Action {
			return actions[i].Action < actions[j].Action
		}
		return actions[i].Name < actions[j].Name
	})
	return actions, nil
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
	manifest := filepath.Join(home, ".agents", "skills.toml")
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
