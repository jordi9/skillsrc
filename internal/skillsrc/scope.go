package skillsrc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type CLIOptions struct {
	WorkingDir string
	HomeDir    string
	CacheDir   string
	LockDir    string
	GitBinary  string
	Out        io.Writer
	Err        io.Writer
}

type ScopeRequest struct {
	User             bool
	ManifestPath     string
	ManifestExplicit bool
	LockPath         string
	LockExplicit     bool
	TargetDir        string
	TargetExplicit   bool
}

type Layout struct {
	ProjectRoot  string
	ManifestPath string
	LockPath     string
	TargetDir    string
}

func DefaultCLIOptions() (CLIOptions, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return CLIOptions{}, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return CLIOptions{}, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return CLIOptions{}, err
	}
	return CLIOptions{
		WorkingDir: workingDir,
		HomeDir:    homeDir,
		CacheDir:   filepath.Join(cacheDir, "skillsrc", "repos"),
		LockDir:    filepath.Join(cacheDir, "skillsrc", "locks"),
		GitBinary:  "git",
		Out:        os.Stdout,
		Err:        os.Stderr,
	}, nil
}

func FindProjectManifest(startDir, homeDir string) (string, error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	start = filepath.Clean(start)
	home, err := filepath.Abs(homeDir)
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	home = filepath.Clean(home)
	insideHome := start == home || isWithin(start, home)
	userManifest := filepath.Join(home, ".agents", "skills.toml")
	for directory := start; ; directory = filepath.Dir(directory) {
		if insideHome && directory == home {
			break
		}
		manifest := filepath.Join(directory, "skills.toml")
		if manifest == userManifest {
			continue
		}
		if info, statErr := os.Stat(manifest); statErr == nil {
			if info.IsDir() {
				return "", fmt.Errorf("project manifest %q is a directory", manifest)
			}
			return manifest, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project manifest %q: %w", manifest, statErr)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", os.ErrNotExist
}

func ResolveLayout(request ScopeRequest, runtime CLIOptions) (Layout, error) {
	if request.User && request.ManifestExplicit {
		return Layout{}, errors.New("--global/--user cannot be combined with --manifest")
	}
	if request.User {
		manifest := filepath.Join(runtime.HomeDir, ".agents", "skills.toml")
		layout := derivedLayout("", manifest, filepath.Join(runtime.HomeDir, ".agents", "skills"), runtime.WorkingDir, request)
		if _, err := os.Stat(layout.ManifestPath); errors.Is(err, os.ErrNotExist) {
			return Layout{}, fmt.Errorf("user manifest not found at %q; run skillsrc -g init", layout.ManifestPath)
		} else if err != nil {
			return Layout{}, fmt.Errorf("inspect user manifest %q: %w", layout.ManifestPath, err)
		}
		return layout, nil
	}
	if request.ManifestExplicit {
		manifest := resolveFromWorkingDir(request.ManifestPath, runtime.WorkingDir)
		layout := derivedLayout("", manifest, filepath.Join(filepath.Dir(manifest), ".agents", "skills"), runtime.WorkingDir, request)
		if _, err := os.Stat(layout.ManifestPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return Layout{}, fmt.Errorf("manifest not found at %q; run skillsrc --manifest %s init", layout.ManifestPath, request.ManifestPath)
			}
			return Layout{}, fmt.Errorf("inspect manifest %q: %w", layout.ManifestPath, err)
		}
		return layout, nil
	}
	manifest, err := FindProjectManifest(runtime.WorkingDir, runtime.HomeDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Layout{}, errors.New("no project skills.toml found; run skillsrc init or use --global")
		}
		return Layout{}, err
	}
	root := filepath.Dir(manifest)
	return derivedLayout(root, manifest, filepath.Join(root, ".agents", "skills"), runtime.WorkingDir, request), nil
}

func ResolveInitLayout(request ScopeRequest, runtime CLIOptions) (Layout, error) {
	if request.User && request.ManifestExplicit {
		return Layout{}, errors.New("--global/--user cannot be combined with --manifest")
	}
	if request.User {
		manifest := filepath.Join(runtime.HomeDir, ".agents", "skills.toml")
		return derivedLayout("", manifest, filepath.Join(runtime.HomeDir, ".agents", "skills"), runtime.WorkingDir, request), nil
	}
	if request.ManifestExplicit {
		manifest := resolveFromWorkingDir(request.ManifestPath, runtime.WorkingDir)
		return derivedLayout("", manifest, filepath.Join(filepath.Dir(manifest), ".agents", "skills"), runtime.WorkingDir, request), nil
	}
	workingDir, err := filepath.Abs(runtime.WorkingDir)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve working directory: %w", err)
	}
	homeDir, err := filepath.Abs(runtime.HomeDir)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve home directory: %w", err)
	}
	workingDir, homeDir = filepath.Clean(workingDir), filepath.Clean(homeDir)
	if workingDir == homeDir {
		return Layout{}, errors.New("cannot initialize a project directly in $HOME; use skillsrc --global init")
	}
	if existing, findErr := FindProjectManifest(workingDir, homeDir); findErr == nil {
		if filepath.Dir(existing) == workingDir {
			return Layout{}, fmt.Errorf("project is already initialized at %q", existing)
		}
		return Layout{}, fmt.Errorf("cannot initialize here; project already uses manifest %q", existing)
	} else if !errors.Is(findErr, os.ErrNotExist) {
		return Layout{}, findErr
	}
	manifest := filepath.Join(workingDir, "skills.toml")
	return derivedLayout(workingDir, manifest, filepath.Join(workingDir, ".agents", "skills"), runtime.WorkingDir, request), nil
}

func derivedLayout(projectRoot, manifest, defaultTarget, workingDir string, request ScopeRequest) Layout {
	lockPath := filepath.Join(filepath.Dir(manifest), "skills.lock")
	if request.LockExplicit {
		lockPath = resolveFromWorkingDir(request.LockPath, workingDir)
	}
	targetDir := defaultTarget
	if request.TargetExplicit {
		targetDir = resolveFromWorkingDir(request.TargetDir, workingDir)
	}
	return Layout{ProjectRoot: projectRoot, ManifestPath: manifest, LockPath: lockPath, TargetDir: targetDir}
}

func resolveFromWorkingDir(path, base string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
