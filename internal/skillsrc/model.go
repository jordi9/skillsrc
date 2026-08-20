package skillsrc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const SchemaVersion = 1

type SourceKind string

const (
	SourceGit   SourceKind = "git"
	SourceLocal SourceKind = "local"
)

type Manifest struct {
	Version int              `toml:"version"`
	Sources []ManifestSource `toml:"sources"`
	Path    string           `toml:"-"`
}

type ManifestSource struct {
	Repo                   string          `toml:"repo,omitempty"`
	Path                   string          `toml:"path,omitempty"`
	Ref                    string          `toml:"ref,omitempty"`
	Plugin                 string          `toml:"plugin,omitempty"`
	Skills                 []string        `toml:"skills,omitempty"`
	DisableModelInvocation map[string]bool `toml:"-"`
	ResolvedPath           string          `toml:"-"`
}

type Lock struct {
	Version int          `toml:"version"`
	Sources []LockSource `toml:"sources"`
}

type LockSource struct {
	Kind     SourceKind    `toml:"kind"`
	Identity string        `toml:"identity"`
	Repo     string        `toml:"repo,omitempty"`
	Path     string        `toml:"path,omitempty"`
	Ref      string        `toml:"ref,omitempty"`
	Plugin   string        `toml:"plugin,omitempty"`
	Commit   string        `toml:"commit,omitempty"`
	Skills   []LockedSkill `toml:"skills"`
}

type LockedSkill struct {
	Name                          string `toml:"name"`
	Path                          string `toml:"path"`
	Hash                          string `toml:"hash"`
	SourceDisablesModelInvocation bool   `toml:"disable_model_invocation,omitempty"`
}

type ValidationError struct{ Problem string }

func (e *ValidationError) Error() string { return "validation: " + e.Problem }

type mapOrStringSkill struct {
	Name                   string
	DisableModelInvocation bool
}

func (skill *mapOrStringSkill) UnmarshalTOML(value any) error {
	switch value := value.(type) {
	case string:
		skill.Name = value
		if strings.HasPrefix(value, "!") {
			skill.Name = strings.TrimPrefix(value, "!")
			skill.DisableModelInvocation = true
		}
		return nil
	case map[string]any:
		for key := range value {
			if key != "name" && key != "disable-model-invocation" {
				return &ValidationError{Problem: fmt.Sprintf("unknown skill key %q", key)}
			}
		}
		name, ok := value["name"].(string)
		if !ok {
			return &ValidationError{Problem: "inline skill must have a string name"}
		}
		disabled := false
		if raw, exists := value["disable-model-invocation"]; exists {
			var valid bool
			disabled, valid = raw.(bool)
			if !valid {
				return &ValidationError{Problem: fmt.Sprintf("skill %q disable-model-invocation must be a boolean", name)}
			}
		}
		skill.Name, skill.DisableModelInvocation = name, disabled
		return nil
	default:
		return &ValidationError{Problem: "skill entry must be a string or inline object"}
	}
}

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func LoadManifest(path string) (Manifest, error) {
	var raw struct {
		Version int `toml:"version"`
		Sources []struct {
			Repo   string             `toml:"repo,omitempty"`
			Path   string             `toml:"path,omitempty"`
			Ref    string             `toml:"ref,omitempty"`
			Plugin string             `toml:"plugin,omitempty"`
			Skills []mapOrStringSkill `toml:"skills"`
		} `toml:"sources"`
	}
	metadata, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %q: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return Manifest{}, &ValidationError{Problem: fmt.Sprintf("unknown manifest key %q", undecoded[0])}
	}
	manifest := Manifest{Version: raw.Version, Sources: make([]ManifestSource, len(raw.Sources))}
	for i, source := range raw.Sources {
		manifest.Sources[i] = ManifestSource{Repo: source.Repo, Path: source.Path, Ref: source.Ref, Plugin: source.Plugin, DisableModelInvocation: make(map[string]bool)}
		for _, skill := range source.Skills {
			manifest.Sources[i].Skills = append(manifest.Sources[i].Skills, skill.Name)
			if skill.DisableModelInvocation {
				manifest.Sources[i].DisableModelInvocation[skill.Name] = true
			}
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return manifest, fmt.Errorf("resolve manifest path: %w", err)
	}
	manifest.Path = filepath.Clean(absolute)
	if err := validateManifest(&manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func validateManifest(manifest *Manifest) error {
	if manifest.Version != SchemaVersion {
		return &ValidationError{Problem: fmt.Sprintf("manifest version must be %d", SchemaVersion)}
	}
	seen := make(map[string]struct{})
	for i := range manifest.Sources {
		source := &manifest.Sources[i]
		hasRepo, hasPath := strings.TrimSpace(source.Repo) != "", strings.TrimSpace(source.Path) != ""
		if hasRepo == hasPath {
			return &ValidationError{Problem: fmt.Sprintf("source %d must declare exactly one of repo or path", i+1)}
		}
		if hasPath && source.Ref != "" {
			return &ValidationError{Problem: fmt.Sprintf("source %d: local source cannot set ref", i+1)}
		}
		if source.Plugin != "" && (!skillNamePattern.MatchString(source.Plugin) || source.Plugin == "." || source.Plugin == "..") {
			return &ValidationError{Problem: fmt.Sprintf("source %d has invalid plugin %q", i+1, source.Plugin)}
		}
		if len(source.Skills) == 0 && source.Plugin == "" {
			return &ValidationError{Problem: fmt.Sprintf("source %d must select at least one skill or a plugin", i+1)}
		}
		for _, name := range source.Skills {
			if !skillNamePattern.MatchString(name) || name == "." || name == ".." {
				return &ValidationError{Problem: fmt.Sprintf("invalid skill name %q", name)}
			}
			if _, exists := seen[name]; exists {
				return &ValidationError{Problem: fmt.Sprintf("duplicate skill %q", name)}
			}
			seen[name] = struct{}{}
		}
		if hasPath {
			resolved, err := resolveLocalPath(filepath.Dir(manifest.Path), source.Path)
			if err != nil {
				return &ValidationError{Problem: fmt.Sprintf("source %d: %v", i+1, err)}
			}
			source.ResolvedPath = resolved
		}
	}
	return nil
}

func resolveLocalPath(manifestDir, path string) (string, error) {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(manifestDir, path)
	}
	return filepath.Clean(path), nil
}

func LoadLock(path string) (Lock, error) {
	var lock Lock
	metadata, err := toml.DecodeFile(path, &lock)
	if errors.Is(err, os.ErrNotExist) {
		return Lock{Version: SchemaVersion}, nil
	}
	if err != nil {
		return lock, fmt.Errorf("read lock %q: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return lock, &ValidationError{Problem: fmt.Sprintf("unknown lock key %q", undecoded[0])}
	}
	if err := ValidateLock(lock); err != nil {
		return lock, err
	}
	return lock, nil
}

func ValidateLock(lock Lock) error {
	if lock.Version != SchemaVersion {
		return &ValidationError{Problem: fmt.Sprintf("lock version must be %d", SchemaVersion)}
	}
	seen := make(map[string]struct{})
	for i, source := range lock.Sources {
		if err := validateLockSource(source, i+1); err != nil {
			return err
		}
		for _, skill := range source.Skills {
			if !skillNamePattern.MatchString(skill.Name) || skill.Path == "" || !strings.HasPrefix(skill.Hash, "sha256:") {
				return &ValidationError{Problem: fmt.Sprintf("invalid locked skill %q", skill.Name)}
			}
			if skill.Path != "." {
				if err := ValidateRelativeSkillPath(skill.Path); err != nil {
					return err
				}
			}
			if _, ok := seen[skill.Name]; ok {
				return &ValidationError{Problem: fmt.Sprintf("duplicate locked skill %q", skill.Name)}
			}
			seen[skill.Name] = struct{}{}
		}
	}
	return nil
}

func validateLockSource(source LockSource, position int) error {
	if source.Plugin != "" && (!skillNamePattern.MatchString(source.Plugin) || source.Plugin == "." || source.Plugin == "..") {
		return &ValidationError{Problem: fmt.Sprintf("lock source %d has invalid plugin %q", position, source.Plugin)}
	}
	if source.Kind != SourceGit && source.Kind != SourceLocal {
		return &ValidationError{Problem: fmt.Sprintf("lock source %d has invalid kind %q", position, source.Kind)}
	}
	if source.Identity == "" {
		return &ValidationError{Problem: fmt.Sprintf("lock source %d has no identity", position)}
	}
	if source.Kind == SourceGit && (source.Repo == "" || source.Commit == "" || source.Path != "") {
		return &ValidationError{Problem: fmt.Sprintf("lock Git source %d has inconsistent fields", position)}
	}
	if source.Kind == SourceLocal && (source.Path == "" || source.Repo != "" || source.Ref != "" || source.Commit != "") {
		return &ValidationError{Problem: fmt.Sprintf("lock local source %d has inconsistent fields", position)}
	}
	return nil
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	type encodedSource struct {
		Repo   string   `toml:"repo,omitempty"`
		Path   string   `toml:"path,omitempty"`
		Ref    string   `toml:"ref,omitempty"`
		Plugin string   `toml:"plugin,omitempty"`
		Skills []string `toml:"skills,omitempty"`
	}
	stable := struct {
		Version int             `toml:"version"`
		Sources []encodedSource `toml:"sources,omitempty"`
	}{Version: manifest.Version, Sources: make([]encodedSource, len(manifest.Sources))}
	for i, source := range manifest.Sources {
		stable.Sources[i] = encodedSource{Repo: source.Repo, Path: source.Path, Ref: source.Ref, Plugin: source.Plugin}
		for _, name := range source.Skills {
			if source.DisableModelInvocation[name] {
				name = "!" + name
			}
			stable.Sources[i].Skills = append(stable.Sources[i].Skills, name)
		}
		sort.Slice(stable.Sources[i].Skills, func(a, b int) bool {
			return strings.TrimPrefix(stable.Sources[i].Skills[a], "!") < strings.TrimPrefix(stable.Sources[i].Skills[b], "!")
		})
	}
	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(stable); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buffer.Bytes(), nil
}

func EncodeLock(lock Lock) ([]byte, error) {
	stable := lock
	stable.Sources = append([]LockSource(nil), lock.Sources...)
	for i := range stable.Sources {
		stable.Sources[i].Skills = append([]LockedSkill(nil), stable.Sources[i].Skills...)
		sort.Slice(stable.Sources[i].Skills, func(a, b int) bool {
			return stable.Sources[i].Skills[a].Name < stable.Sources[i].Skills[b].Name
		})
	}
	sort.Slice(stable.Sources, func(i, j int) bool {
		left, right := stable.Sources[i], stable.Sources[j]
		return left.Identity+"\x00"+left.Ref+"\x00"+left.Plugin+"\x00"+left.Repo+"\x00"+left.Path < right.Identity+"\x00"+right.Ref+"\x00"+right.Plugin+"\x00"+right.Repo+"\x00"+right.Path
	})
	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(stable); err != nil {
		return nil, fmt.Errorf("encode lock: %w", err)
	}
	return buffer.Bytes(), nil
}

func LocalIdentity(path string) string {
	return "local:" + filepath.ToSlash(filepath.Clean(path))
}
