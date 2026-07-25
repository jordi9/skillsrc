package skillsrc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scopeRuntime(workingDir, homeDir string) CLIOptions {
	return CLIOptions{WorkingDir: workingDir, HomeDir: homeDir, CacheDir: filepath.Join(homeDir, "cache", "repos"), LockDir: filepath.Join(homeDir, "cache", "locks"), GitBinary: "git", Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
}

func TestFindProjectManifestUsesNearestAncestor(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "project")
	nestedProject := filepath.Join(root, "packages", "nested")
	cwd := filepath.Join(nestedProject, "src")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	writeTestFile(t, filepath.Join(root, "skills.toml"), "version = 1\n")
	writeTestFile(t, filepath.Join(nestedProject, "skills.toml"), "version = 1\n")

	manifest, err := FindProjectManifest(cwd, home)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(nestedProject, "skills.toml"), manifest)
}

func TestFindProjectManifestDoesNotInspectHomeManifest(t *testing.T) {
	home := t.TempDir()
	for _, relative := range []string{"project/nested", "x"} {
		cwd := filepath.Join(home, relative)
		require.NoError(t, os.MkdirAll(cwd, 0o755))
		writeTestFile(t, filepath.Join(home, "skills.toml"), "version = 1\n")

		_, err := FindProjectManifest(cwd, home)
		assert.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestFindProjectManifestOutsideHomeSearchesToRootBoundary(t *testing.T) {
	outside := t.TempDir()
	home := t.TempDir()
	cwd := filepath.Join(outside, "a", "b")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	writeTestFile(t, filepath.Join(outside, "skills.toml"), "version = 1\n")

	manifest, err := FindProjectManifest(cwd, home)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(outside, "skills.toml"), manifest)
}

func TestResolveLayoutProjectAndExplicitOverrides(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "project")
	cwd := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version = 1\n")
	runtime := scopeRuntime(cwd, home)

	layout, err := ResolveLayout(ScopeRequest{}, runtime)
	require.NoError(t, err)
	assert.Equal(t, root, layout.ProjectRoot)
	assert.Equal(t, manifest, layout.ManifestPath)
	assert.Equal(t, filepath.Join(root, "skills.lock"), layout.LockPath)
	assert.Equal(t, filepath.Join(root, ".agents", "skills"), layout.TargetDir)

	explicit := filepath.Join(home, "other", "custom.toml")
	writeTestFile(t, explicit, "version = 1\n")
	layout, err = ResolveLayout(ScopeRequest{ManifestPath: explicit, ManifestExplicit: true, LockPath: filepath.Join(home, "locks", "x.lock"), LockExplicit: true, TargetDir: filepath.Join(home, "target"), TargetExplicit: true}, runtime)
	require.NoError(t, err)
	assert.Empty(t, layout.ProjectRoot)
	assert.Equal(t, explicit, layout.ManifestPath)
	assert.Equal(t, filepath.Join(home, "locks", "x.lock"), layout.LockPath)
	assert.Equal(t, filepath.Join(home, "target"), layout.TargetDir)
}

func TestResolveLayoutUserAndConflict(t *testing.T) {
	home := t.TempDir()
	manifest := filepath.Join(home, ".agents", "skills.toml")
	writeTestFile(t, manifest, "version = 1\n")
	runtime := scopeRuntime(filepath.Join(home, "work"), home)

	layout, err := ResolveLayout(ScopeRequest{User: true}, runtime)
	require.NoError(t, err)
	assert.Empty(t, layout.ProjectRoot)
	assert.Equal(t, manifest, layout.ManifestPath)
	assert.Equal(t, filepath.Join(home, ".agents", "skills.lock"), layout.LockPath)
	assert.Equal(t, filepath.Join(home, ".agents", "skills"), layout.TargetDir)

	_, err = ResolveLayout(ScopeRequest{User: true, ManifestExplicit: true, ManifestPath: "x"}, runtime)
	assert.ErrorContains(t, err, "cannot be combined")
}

func TestResolveLayoutMissingDiagnostics(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	runtime := scopeRuntime(cwd, home)

	_, err := ResolveLayout(ScopeRequest{}, runtime)
	assert.ErrorContains(t, err, "skillsrc init")
	assert.ErrorContains(t, err, "--manifest")
	assert.ErrorContains(t, err, "--global")
	_, err = ResolveLayout(ScopeRequest{User: true}, runtime)
	assert.ErrorContains(t, err, "skillsrc -g init")
}

func TestResolveInitLayoutScopesAndRefusals(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "project")
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	layout, err := ResolveInitLayout(ScopeRequest{}, scopeRuntime(root, home))
	require.NoError(t, err)
	assert.Equal(t, root, layout.ProjectRoot)
	assert.Equal(t, filepath.Join(root, "skills.toml"), layout.ManifestPath)

	_, err = ResolveInitLayout(ScopeRequest{}, scopeRuntime(home, home))
	assert.ErrorContains(t, err, "--global")

	writeTestFile(t, filepath.Join(root, "skills.toml"), "version = 1\n")
	_, err = ResolveInitLayout(ScopeRequest{}, scopeRuntime(nested, home))
	assert.ErrorContains(t, err, "project already uses manifest")

	layout, err = ResolveInitLayout(ScopeRequest{User: true}, scopeRuntime(root, home))
	require.NoError(t, err)
	assert.Empty(t, layout.ProjectRoot)
	assert.Equal(t, filepath.Join(home, ".agents", "skills.toml"), layout.ManifestPath)

	layout, err = ResolveInitLayout(ScopeRequest{ManifestExplicit: true, ManifestPath: "config/custom.toml"}, scopeRuntime(root, home))
	require.NoError(t, err)
	assert.Empty(t, layout.ProjectRoot)
	assert.Equal(t, filepath.Join(root, "config", "custom.toml"), layout.ManifestPath)
}
