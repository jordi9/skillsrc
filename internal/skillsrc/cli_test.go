package skillsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultOptionsUseAgentsSkillsrcManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	options, err := DefaultOptions()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".agents", "skills.toml"), options.ManifestPath)
	assert.Equal(t, filepath.Join(home, ".agents", "skills.lock"), options.LockPath)
	assert.Equal(t, filepath.Join(home, ".agents", "skills"), options.TargetDir)
}

func TestCLIEndToEndWithJSONListAndDoctor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "installed: one")
	assert.Contains(t, output.String(), "1 installed, 0 repaired, 0 unchanged, 0 pruned; 0 repositories fetched")
	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, RunCLI(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "0 installed, 0 repaired, 1 unchanged, 0 pruned; 0 repositories fetched")
	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, RunCLI(context.Background(), []string{"list", "--json"}, options), errorOutput.String())
	var statuses []SkillStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &statuses))
	require.Len(t, statuses, 1)
	assert.Equal(t, "current", statuses[0].Status)

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, RunCLI(context.Background(), []string{"doctor", "--json"}, options), errorOutput.String())
	var report DoctorReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Empty(t, report.Issues)
}

func TestCLISyncExplainsRepositoryFetch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, _ := makeGitRemote(t, map[string]string{"one/SKILL.md": "---\nname: one\n---\n"})
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\"main\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "fetched "+remote+": new or changed declaration")
	assert.Contains(t, output.String(), "1 repositories fetched")
}

func TestListDisplayIsCompact(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "jordi9/skills", displaySource("git@github.com:jordi9/skills"))
	assert.Equal(t, "owner/repo", displaySource("https://github.com/owner/repo.git"))
	assert.Equal(t, "main @ 0123456789ab", displayRevision(SkillStatus{ConfiguredRef: "main", LockedCommit: "0123456789abcdef", Status: "current"}))
	assert.Equal(t, "local", displayRevision(SkillStatus{Status: "current"}))
	assert.Equal(t, "✓ synced", displayState("current"))
	assert.Equal(t, "! modified", displayState("drifted"))
}

func TestCLIAddSourceListsAvailableSkillsWithoutChangingManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "two"), "two", "body")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifest := filepath.Join(root, "skills.toml")
	original := []byte("version = 1\n")
	require.NoError(t, os.WriteFile(manifest, original, 0o644))
	options := testOptions(root, manifest)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"add", local}, options), errorOutput.String())
	assert.Contains(t, output.String(), "Available skills in "+local+":\n  one\n  two\n")
	actual, err := os.ReadFile(manifest)
	require.NoError(t, err)
	assert.Equal(t, original, actual)
	assert.NoFileExists(t, options.LockPath)
}

func TestCLIAddAllFetchesRepositoryOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remotePath, _ := makeGitRemote(t, map[string]string{
		"one/SKILL.md": "---\nname: one\n---\n",
		"two/SKILL.md": "---\nname: two\n---\n",
	})
	remote := "file://" + remotePath
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"add", remote, "--all"}, options), errorOutput.String())
	assert.Equal(t, 1, strings.Count(output.String(), "fetched "+remote+":"), output.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"one", "two"}, manifest.Sources[0].Skills)
	assert.FileExists(t, filepath.Join(options.TargetDir, "one", "SKILL.md"))
	assert.FileExists(t, filepath.Join(options.TargetDir, "two", "SKILL.md"))

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, RunCLI(context.Background(), []string{"remove", "two"}, options), errorOutput.String())
	assert.NotContains(t, output.String(), "fetched ", output.String())
	assert.FileExists(t, filepath.Join(options.TargetDir, "one", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(options.TargetDir, "two"))
}

func TestConcurrentAddsPreserveBothDeclarations(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	makeSkill(t, filepath.Join(local, "two"), "two", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")

	start := make(chan struct{})
	codes := make(chan int, 2)
	for _, name := range []string{"one", "two"} {
		name := name
		go func() {
			<-start
			options := testOptions(root, manifestPath)
			options.TargetDir = filepath.Join(root, "targets", name)
			options.Out, options.Err = io.Discard, io.Discard
			codes <- RunCLI(context.Background(), []string{"add", local, name}, options)
		}()
	}
	close(start)
	assert.Equal(t, 0, <-codes)
	assert.Equal(t, 0, <-codes)
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"one", "two"}, manifest.Sources[0].Skills)
	lock, err := LoadLock(filepath.Join(root, "skills.lock"))
	require.NoError(t, err)
	require.Len(t, lock.Sources, 1)
	require.Len(t, lock.Sources[0].Skills, 2)
}

func TestConcurrentManifestsCannotOverwriteSharedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "shared-target")
	type outcome struct {
		name string
		code int
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for _, name := range []string{"first", "second"} {
		name := name
		manifestDir := filepath.Join(root, name)
		local := filepath.Join(manifestDir, "source")
		makeSkill(t, filepath.Join(local, "shared"), "shared", name)
		manifestPath := filepath.Join(manifestDir, "skills.toml")
		writeTestFile(t, manifestPath, "version = 1\n")
		go func() {
			<-start
			options := testOptions(manifestDir, manifestPath)
			options.TargetDir = target
			options.Out, options.Err = io.Discard, io.Discard
			outcomes <- outcome{name: name, code: RunCLI(context.Background(), []string{"add", local, "shared"}, options)}
		}()
	}
	close(start)
	first, second := <-outcomes, <-outcomes
	assert.ElementsMatch(t, []int{0, 1}, []int{first.code, second.code})
	winner := first.name
	if second.code == 0 {
		winner = second.name
	}
	content, err := os.ReadFile(filepath.Join(target, "shared", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), winner)
}

func TestCLIAddKeepsDesiredManifestWhenSyncIsBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "wanted")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	makeSkill(t, filepath.Join(options.TargetDir, "one"), "one", "unmanaged")
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 1, RunCLI(context.Background(), []string{"add", local, "one"}, options))
	assert.Contains(t, errorOutput.String(), "manifest updated; sync incomplete")
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"one"}, manifest.Sources[0].Skills)
	content, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "unmanaged")
	assert.NoFileExists(t, options.LockPath)
}

func TestCLIAddUnknownSkillDoesNotChangeManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	original := []byte("version = 1\n")
	require.NoError(t, os.WriteFile(manifestPath, original, 0o644))
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 1, RunCLI(context.Background(), []string{"add", local, "missing"}, options))
	assert.Contains(t, errorOutput.String(), `selected skill "missing" was not found`)
	actual, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, original, actual)
	assert.NoFileExists(t, options.LockPath)
}

func TestCLIAddAcceptsTagRefAfterSkillName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remotePath, commit := makeGitRemote(t, map[string]string{"one/SKILL.md": "---\nname: one\n---\n"})
	runGitTest(t, "--git-dir="+remotePath, "tag", "v1.0.0", commit)
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"add", "file://" + remotePath, "one", "--ref", "v1.0.0"}, options), errorOutput.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, "v1.0.0", manifest.Sources[0].Ref)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	require.Len(t, lock.Sources, 1)
	assert.Equal(t, commit, lock.Sources[0].Commit)
}

func TestCLIAddCreatesMissingManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"add", local, "one"}, options), errorOutput.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"one"}, manifest.Sources[0].Skills)
}

func TestCLIAddAndRemoveLocalSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, RunCLI(context.Background(), []string{"add", local, "one"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "installed: one")
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, "source", manifest.Sources[0].Path)
	assert.Equal(t, []string{"one"}, manifest.Sources[0].Skills)
	assert.FileExists(t, filepath.Join(options.TargetDir, "one", "SKILL.md"))

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, RunCLI(context.Background(), []string{"remove", "one"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "pruned: one")
	manifest, err = LoadManifest(manifestPath)
	require.NoError(t, err)
	assert.Empty(t, manifest.Sources)
	assert.NoDirExists(t, filepath.Join(options.TargetDir, "one"))
}

func TestCLIManifestFlagUsesSiblingLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifest := filepath.Join(root, "nested", ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"../local\"\nskills=[\"one\"]\n")
	options := testOptions(root, filepath.Join(root, "unused"))
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	code := RunCLI(context.Background(), []string{"--manifest", manifest, "sync"}, options)
	assert.Equal(t, 0, code, errorOutput.String())
	assert.FileExists(t, filepath.Join(filepath.Dir(manifest), "skills.lock"))
}

func TestCLIUnknownCommandIsUsageError(t *testing.T) {
	t.Parallel()
	options := testOptions(t.TempDir(), "/unused")
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput
	assert.Equal(t, 2, RunCLI(context.Background(), []string{"nope"}, options))
	assert.Contains(t, errorOutput.String(), "unknown command")
}
