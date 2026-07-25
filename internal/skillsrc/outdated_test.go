package skillsrc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutdatedReportsRemoteChangeWithoutChangingProjectFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, firstCommit := makeGitRemote(t, map[string]string{
		"one/SKILL.md": "---\nname: one\n---\nfirst\n",
	})
	tagGitCommit(t, remote, firstCommit, "v1.0.0")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\"main\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)

	manifestBefore, err := os.ReadFile(options.ManifestPath)
	require.NoError(t, err)
	lockBefore, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedBefore, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	secondCommit := pushGitChange(t, remote, "one/SKILL.md", "---\nname: one\n---\nsecond\n")
	tagGitCommit(t, remote, secondCommit, "v2.0.0")

	result, err := NewEngine(options).Outdated(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Sources, 1)
	assert.Equal(t, remote, result.Sources[0].Source)
	assert.Equal(t, []string{"one"}, result.Sources[0].Skills)
	assert.Equal(t, firstCommit, result.Sources[0].Old.Commit)
	assert.Equal(t, secondCommit, result.Sources[0].New.Commit)
	assert.NotEmpty(t, result.Sources[0].Old.Date)
	assert.NotEmpty(t, result.Sources[0].New.Date)
	assert.Equal(t, "v1.0.0", result.Sources[0].Old.Tag)
	assert.Equal(t, "v2.0.0", result.Sources[0].New.Tag)
	assert.Len(t, result.Fetches, 1)

	manifestAfter, err := os.ReadFile(options.ManifestPath)
	require.NoError(t, err)
	lockAfter, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedAfter, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, manifestBefore, manifestAfter)
	assert.Equal(t, lockBefore, lockAfter)
	assert.Equal(t, installedBefore, installedAfter)
}

func TestOutdatedReportsUnchangedLocalSourceWithoutChangingProjectFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	manifestBefore, err := os.ReadFile(options.ManifestPath)
	require.NoError(t, err)
	lockBefore, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedPath := filepath.Join(options.TargetDir, "one", "SKILL.md")
	installedBefore, err := os.ReadFile(installedPath)
	require.NoError(t, err)

	result, err := NewEngine(options).Outdated(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Fetches)
	assert.Empty(t, result.Sources)
	assert.Equal(t, []LocalOutdatedSource{{Source: "./local"}}, result.LocalSources)
	manifestAfter, err := os.ReadFile(options.ManifestPath)
	require.NoError(t, err)
	lockAfter, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedAfter, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	assert.Equal(t, manifestBefore, manifestAfter)
	assert.Equal(t, lockBefore, lockAfter)
	assert.Equal(t, installedBefore, installedAfter)
}

func TestOutdatedReportsChangedLocalSkillsDeterministically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "zeta"), "zeta", "first")
	makeSkill(t, filepath.Join(local, "alpha"), "alpha", "first")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"zeta\", \"alpha\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	lockBefore, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedPath := filepath.Join(options.TargetDir, "alpha", "SKILL.md")
	installedBefore, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	makeSkill(t, filepath.Join(local, "zeta"), "zeta", "second")
	makeSkill(t, filepath.Join(local, "alpha"), "alpha", "second")

	result, err := NewEngine(options).Outdated(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []LocalOutdatedSource{{Source: "./local", ChangedSkills: []string{"alpha", "zeta"}}}, result.LocalSources)
	lockAfter, err := os.ReadFile(options.LockPath)
	require.NoError(t, err)
	installedAfter, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	assert.Equal(t, lockBefore, lockAfter)
	assert.Equal(t, installedBefore, installedAfter)
}

func TestOutdatedTreatsMissingLocalLockEntryAsChangedAndHonorsSelectors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	makeSkill(t, filepath.Join(first, "one"), "one", "body")
	makeSkill(t, filepath.Join(second, "two"), "two", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./first\"\nskills=[\"one\"]\n[[sources]]\npath=\"./second\"\nskills=[\"two\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	lock.Sources[1].Skills = nil
	encoded, err := EncodeLock(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(options.LockPath, encoded, 0o644))

	for _, selector := range []string{"./second", "two"} {
		result, err := NewEngine(options).Outdated(context.Background(), []string{selector})
		require.NoError(t, err)
		assert.Equal(t, []LocalOutdatedSource{{Source: "./second", ChangedSkills: []string{"two"}}}, result.LocalSources)
	}
}

func TestOutdatedMatchesDisjointManifestSourcesUsingTheSameLocalPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	makeSkill(t, filepath.Join(local, "two"), "two", "body")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n[[sources]]\npath=\"./local\"\nskills=[\"two\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)

	result, err := NewEngine(options).Outdated(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []LocalOutdatedSource{{Source: "./local"}, {Source: "./local"}}, result.LocalSources)
}

func TestOutdatedTreatsInconsistentLocalInvocationMetadataAsChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	writeTestFile(t, filepath.Join(local, "one", "SKILL.md"), "---\nname: one\ndisable-model-invocation: true\n---\nbody\n")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	lock.Sources[0].Skills[0].SourceDisablesModelInvocation = false
	encoded, err := EncodeLock(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(options.LockPath, encoded, 0o644))

	result, err := NewEngine(options).Outdated(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []LocalOutdatedSource{{Source: "./local", ChangedSkills: []string{"one"}}}, result.LocalSources)
}

func TestCLIOutdatedShowsAvailableUpdate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, firstCommit := makeGitRemote(t, map[string]string{
		"one/SKILL.md": "---\nname: one\n---\nfirst\n",
		"two/SKILL.md": "---\nname: two\n---\nfirst\n",
	})
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\"main\"\nskills=[\"one\", \"two\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)

	var output, errorOutput bytes.Buffer
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"outdated"}, options, &output, &errorOutput), errorOutput.String())
	displayedSource := displaySource(remote, filepath.Dir(root))
	assert.Contains(t, output.String(), "✓ "+displayedSource+" · up to date")
	assert.NotContains(t, output.String(), " · fetched")

	output.Reset()
	errorOutput.Reset()
	secondCommit := pushGitChange(t, remote, "one/SKILL.md", "---\nname: one\n---\nsecond\n")
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"outdated"}, options, &output, &errorOutput), errorOutput.String())
	pattern := `↑ ` + regexp.QuoteMeta(displayedSource) + ` · one, two · update available · \d{4}-\d{2}-\d{2} \(` + firstCommit[:12] + `\) → \d{4}-\d{2}-\d{2} \(` + secondCommit[:12] + `\)`
	assert.Regexp(t, pattern, output.String())
	assert.NotContains(t, output.String(), " · fetched")
	assert.Contains(t, output.String(), "└─ Summary · 1 update available")
}

func TestCLIOutdatedShowsLocalContentStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "first")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifestPath)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	var output, errorOutput bytes.Buffer

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"outdated"}, options, &output, &errorOutput), errorOutput.String())
	assert.Contains(t, output.String(), "✓ ./local · up to date")

	makeSkill(t, filepath.Join(local, "one"), "one", "second")
	output.Reset()
	errorOutput.Reset()
	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"outdated"}, options, &output, &errorOutput), errorOutput.String())
	assert.Contains(t, output.String(), "• ./local · one · local changes not synced")
	assert.Contains(t, output.String(), "└─ Summary · 1 local source changed")
	assert.NotContains(t, output.String(), "update available")
}

func TestDisplayRevisionMetadataPrefersExactTag(t *testing.T) {
	t.Parallel()
	commit := "0123456789abcdef0123456789abcdef01234567"
	assert.Equal(t, "v2.0.0 (0123456789ab)", displayRevisionMetadata(GitRevision{Commit: commit, Date: "2026-07-25", Tag: "v2.0.0"}))
	assert.Equal(t, "2026-07-25 (0123456789ab)", displayRevisionMetadata(GitRevision{Commit: commit, Date: "2026-07-25"}))
}

func tagGitCommit(t *testing.T, remote, commit, tag string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "tag-work")
	runGitTest(t, "clone", remote, work)
	runGitTest(t, "-C", work, "tag", tag, commit)
	runGitTest(t, "-C", work, "push", "origin", tag)
}
