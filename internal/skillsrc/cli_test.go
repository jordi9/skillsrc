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

func TestDefaultCLIOptionsUseRuntimeDirectories(t *testing.T) {
	home := t.TempDir()
	workingDir := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(workingDir, 0o755))
	t.Setenv("HOME", home)
	t.Chdir(workingDir)
	options, err := DefaultCLIOptions()
	require.NoError(t, err)
	assert.Equal(t, workingDir, options.WorkingDir)
	assert.Equal(t, home, options.HomeDir)
	assert.Equal(t, "git", options.GitBinary)
}

func runCLIResolved(ctx context.Context, args []string, options Options) int {
	runtime := CLIOptions{
		WorkingDir: filepath.Dir(options.ManifestPath),
		HomeDir:    filepath.Dir(filepath.Dir(options.ManifestPath)),
		CacheDir:   options.CacheDir,
		LockDir:    filepath.Join(filepath.Dir(options.TargetDir), ".test-locks"),
		GitBinary:  options.GitBinary,
		Out:        options.Out,
		Err:        options.Err,
	}
	flags := []string{"--manifest", options.ManifestPath, "--lock", options.LockPath, "--target", options.TargetDir}
	return RunCLI(ctx, append(flags, args...), runtime)
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "✓ one · installed\n\n└─ Sync complete · 1 installed")
	assert.NotContains(t, output.String(), "  ✓")
	assert.NotContains(t, output.String(), "  └─")
	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Equal(t, "\n└─ Sync complete · 1 up to date\n", output.String())
	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"list", "--json"}, options), errorOutput.String())
	var statuses []SkillStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &statuses))
	require.Len(t, statuses, 1)
	assert.Equal(t, "enabled", statuses[0].ModelInvocation)
	assert.Equal(t, "current", statuses[0].Status)

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"doctor", "--json"}, options), errorOutput.String())
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"sync"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "↓ "+displaySource(remote, filepath.Dir(root))+" · fetched\n")
	assert.Contains(t, output.String(), "1 repository fetched")
}

func TestStyleSummaryColorsTheWholeLinePink(t *testing.T) {
	line := "└─ Sync complete · 1 installed"
	assert.Equal(t, line, styleSummary(line, false))
	assert.Equal(t, "\x1b[35m"+line+"\x1b[0m", styleSummary(line, true))
}

func TestListDisplayShowsModelInvocationProvenance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "enabled"), "enabled", "body")
	makeSkill(t, filepath.Join(local, "configured"), "configured", "body")
	writeTestFile(t, filepath.Join(local, "upstream", "SKILL.md"), "---\nname: upstream\ndisable-model-invocation: true\n---\nbody\n")
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"enabled\",\"!configured\",\"!upstream\"]\n")
	options := testOptions(root, manifest)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"sync"}, options), errorOutput.String())
	output.Reset()
	errorOutput.Reset()
	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"list"}, options), errorOutput.String())
	assert.Equal(t, "SKILL         SOURCE   MODEL INVOCATION  REVISION\n─────         ──────   ────────────────  ────────\n✓ configured  ./local  !disabled         local\n✓ enabled     ./local  enabled           local\n✓ upstream    ./local  disabled          local\n\n└─ 3 skills · 3 synced\n", output.String())

	require.NoError(t, os.RemoveAll(local))
	require.NoError(t, os.RemoveAll(filepath.Join(options.TargetDir, "upstream")))
	output.Reset()
	errorOutput.Reset()
	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"list"}, options), errorOutput.String())
	assert.Equal(t, "SKILL         SOURCE   MODEL INVOCATION  REVISION\n─────         ──────   ────────────────  ────────\n✓ configured  ./local  !disabled         local\n✓ enabled     ./local  enabled           local\n✗ upstream    ./local  disabled          local\n  └─ missing — locked skill is not installed\n\n└─ 3 skills · 2 synced · 1 missing\n", output.String())

	output.Reset()
	errorOutput.Reset()
	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"list", "--json"}, options), errorOutput.String())
	var statuses []SkillStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &statuses))
	require.Len(t, statuses, 3)
	assert.Equal(t, "disabled by config", statuses[0].ModelInvocation)
	assert.Equal(t, "enabled", statuses[1].ModelInvocation)
	assert.Equal(t, "disabled by source", statuses[2].ModelInvocation)
	assert.Equal(t, "missing", statuses[2].Status)
}

func TestListDisplayStylesStatusAndRevisionWhenColorIsEnabled(t *testing.T) {
	t.Parallel()
	statuses := []SkillStatus{{Status: "current", ConfiguredRef: "main", LockedCommit: "0123456789abcdef"}}
	plain := "SKILL  SOURCE  MODEL INVOCATION  REVISION\n─────  ──────  ────────────────  ────────\n✓ one  source  enabled           main @ 0123456789ab\n"
	styled := styleListRows(plain, statuses)
	assert.Contains(t, styled, "\x1b[32m✓\x1b[0m one")
	assert.Contains(t, styled, "\x1b[2mmain @ 0123456789ab\x1b[0m")
}

func TestStatusDetailExplainsEveryNonSyncedState(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", statusDetail("current"))
	assert.Equal(t, "modified — managed installation has drifted or been edited", statusDetail("drifted"))
	assert.Equal(t, "blocked — target path conflicts with an unmanaged installation", statusDetail("collision"))
	assert.Equal(t, "missing — locked skill is not installed", statusDetail("missing"))
	assert.Equal(t, "unlocked — declared skill has no consistent lock entry", statusDetail("unlocked"))
}

func TestStyleCommandSuggestions(t *testing.T) {
	t.Parallel()
	message := "no project skills.toml found; run skillsrc init or use --global"
	assert.Equal(t, message, styleCommandSuggestions(message, false))
	assert.Equal(t,
		"no project skills.toml found; run \x1b[1;36mskillsrc init\x1b[0m or use \x1b[1;36m--global\x1b[0m",
		styleCommandSuggestions(message, true),
	)
}

func TestParseAddArgsAcceptsSkillsCLISpecifier(t *testing.T) {
	t.Parallel()

	parsed, err := parseAddArgs([]string{"tldraw/tldraw@write-unit-tests", "--ref", "v1.0.0"})
	require.NoError(t, err)
	assert.Equal(t, "tldraw/tldraw", parsed.source)
	assert.Equal(t, []string{"write-unit-tests"}, parsed.skills)
	assert.Equal(t, "v1.0.0", parsed.ref)
}

func TestParseAddArgsRejectsSkillsCLISpecifierWithOtherSelection(t *testing.T) {
	t.Parallel()

	_, err := parseAddArgs([]string{"tldraw/tldraw@write-unit-tests", "another-skill"})
	assert.EqualError(t, err, "a source using @skill cannot be combined with positional skill names")
}

func TestParseAddArgsAcceptsInvokeUserOnlyForSelectedSkills(t *testing.T) {
	t.Parallel()

	parsed, err := parseAddArgs([]string{"owner/repo@manual-skill", "--invoke-user-only"})
	require.NoError(t, err)
	assert.Equal(t, []string{"manual-skill"}, parsed.skills)
	assert.True(t, parsed.userOnly)
}

func TestParseAddArgsRejectsInvokeUserOnlyWhenOnlyListing(t *testing.T) {
	t.Parallel()

	_, err := parseAddArgs([]string{"owner/repo", "--invoke-user-only"})
	assert.EqualError(t, err, "--invoke-user-only requires skill names or --all")
	_, err = parseAddArgs([]string{"owner/repo", "--list", "--invoke-user-only"})
	assert.EqualError(t, err, "--invoke-user-only cannot be combined with --list")
}

func TestCLIAddUserOnlyWritesOverrideAndInstallsManualSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", local, "one", "--invoke-user-only"}, options), errorOutput.String())
	manifestBytes, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Contains(t, string(manifestBytes), `skills = ["!one"]`)
	installed, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(installed), "disable-model-invocation: true")
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", local}, options), errorOutput.String())
	assert.Contains(t, output.String(), "Available skills from "+displaySource(local, filepath.Dir(root))+":\n  • one\n  • two\n")
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", remote, "--all"}, options), errorOutput.String())
	assert.Equal(t, 1, strings.Count(output.String(), "↓ "+remote+" · fetched\n"), output.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"one", "two"}, manifest.Sources[0].Skills)
	assert.FileExists(t, filepath.Join(options.TargetDir, "one", "SKILL.md"))
	assert.FileExists(t, filepath.Join(options.TargetDir, "two", "SKILL.md"))

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"remove", "two"}, options), errorOutput.String())
	assert.NotContains(t, output.String(), "Fetched ", output.String())
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
			codes <- runCLIResolved(context.Background(), []string{"add", local, name}, options)
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
			outcomes <- outcome{name: name, code: runCLIResolved(context.Background(), []string{"add", local, "shared"}, options)}
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

	assert.Equal(t, 1, runCLIResolved(context.Background(), []string{"add", local, "one"}, options))
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

	assert.Equal(t, 1, runCLIResolved(context.Background(), []string{"add", local, "missing"}, options))
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", "file://" + remotePath, "one", "--ref", "v1.0.0"}, options), errorOutput.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, "v1.0.0", manifest.Sources[0].Ref)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	require.Len(t, lock.Sources, 1)
	assert.Equal(t, commit, lock.Sources[0].Commit)
}

func TestCLIAddRequiresExistingManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 1, runCLIResolved(context.Background(), []string{"add", local, "one"}, options))
	assert.Contains(t, errorOutput.String(), "init")
	assert.NoFileExists(t, manifestPath)
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

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", local, "one"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "✓ one · installed")
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, "source", manifest.Sources[0].Path)
	assert.Equal(t, []string{"one"}, manifest.Sources[0].Skills)
	assert.FileExists(t, filepath.Join(options.TargetDir, "one", "SKILL.md"))

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"remove", "one"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "✓ one · removed")
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

	runtime := CLIOptions{WorkingDir: root, HomeDir: root, CacheDir: options.CacheDir, GitBinary: "git", Out: &output, Err: &errorOutput}
	code := RunCLI(context.Background(), []string{"--manifest", manifest, "--target", options.TargetDir, "sync"}, runtime)
	assert.Equal(t, 0, code, errorOutput.String())
	assert.FileExists(t, filepath.Join(filepath.Dir(manifest), "skills.lock"))
}

func TestCLIUsageErrorsAreConcise(t *testing.T) {
	t.Parallel()
	options := testOptions(t.TempDir(), "/unused")
	var output, errorOutput bytes.Buffer
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 2, runCLIResolved(context.Background(), []string{"nope"}, options))
	assert.Contains(t, errorOutput.String(), "Error: unknown command")
	assert.Equal(t, 1, strings.Count(errorOutput.String(), "Usage:"))
	assert.NotContains(t, errorOutput.String(), "Commands:")

	errorOutput.Reset()
	assert.Equal(t, 2, runCLIResolved(context.Background(), []string{"--bad"}, options))
	assert.Contains(t, errorOutput.String(), "Error: flag provided but not defined: -bad")
	assert.Equal(t, 1, strings.Count(errorOutput.String(), "Usage:"))
	assert.NotContains(t, errorOutput.String(), "Commands:")

	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), nil, options))
	assert.Contains(t, output.String(), "Usage: skillsrc [OPTIONS] <COMMAND>")
	assert.Contains(t, output.String(), "Commands:")
	assert.Empty(t, errorOutput.String())
}
