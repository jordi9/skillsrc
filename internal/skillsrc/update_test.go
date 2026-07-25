package skillsrc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateFetchesAffectedRepositoryOnceAndReportsCommitChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, firstCommit := makeGitRemote(t, map[string]string{
		"one/SKILL.md": "---\nname: one\n---\nfirst\n",
		"two/SKILL.md": "---\nname: two\n---\nfirst\n",
	})
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\"main\"\nskills=[\"one\",\"two\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	secondCommit := pushGitChange(t, remote, "one/SKILL.md", "---\nname: one\n---\nsecond\n")

	result, err := NewEngine(options).Update(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Fetches))
	assert.Contains(t, result.Skills, SkillAction{Name: "one", Action: "updated"})
	var output bytes.Buffer
	printResult(&output, "Update complete", result, root, true)
	assert.Contains(t, output.String(), "✓ one · updated")
	assert.NotContains(t, output.String(), "✓ one · restored")
	require.Len(t, result.Changes, 1)
	assert.Equal(t, firstCommit, result.Changes[0].Old)
	assert.Equal(t, secondCommit, result.Changes[0].New)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	assert.Equal(t, secondCommit, lock.Sources[0].Commit)
	installed, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(installed), "second")
}

func TestUpdateSkipsLocalSourcesButSyncsTheirCurrentContents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "first")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	before, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	makeSkill(t, filepath.Join(local, "one"), "one", "second")

	result, err := NewEngine(options).Update(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, len(result.Fetches))
	assert.Equal(t, []string{"./local"}, result.LocalSkipped)
	after, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	assert.Empty(t, after.Sources[0].Commit)
	assert.Empty(t, after.Sources[0].Ref)
	assert.NotEqual(t, before.Sources[0].Skills[0].Hash, after.Sources[0].Skills[0].Hash)
	installed, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(installed), "second")
}

func TestCLIUpdateDescribesSelectedLocalSourceAsSynced(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "first")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	makeSkill(t, filepath.Join(local, "one"), "one", "second")
	var output, errorOutput strings.Builder
	options.Out, options.Err = &output, &errorOutput

	assert.Equal(t, 0, runCLIResolved(context.Background(), []string{"update", "one"}, options), errorOutput.String())
	assert.Contains(t, output.String(), "✓ ./local · local content synced")
	assert.NotContains(t, output.String(), "local source, skipped")
}

func TestUpdateLeavesCommitPinnedSourceUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, commit := makeGitRemote(t, map[string]string{"one/SKILL.md": "---\nname: one\n---\none\n"})
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\""+commit+"\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)

	result, err := NewEngine(options).Update(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, len(result.Fetches))
	assert.Empty(t, result.Changes)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	assert.Equal(t, commit, lock.Sources[0].Commit)
}

func pushGitChange(t *testing.T, remote, path, content string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	runGitTest(t, "clone", remote, work)
	runGitTest(t, "-C", work, "config", "user.email", "test@example.com")
	runGitTest(t, "-C", work, "config", "user.name", "Test")
	writeTestFile(t, filepath.Join(work, filepath.FromSlash(path)), content)
	runGitTest(t, "-C", work, "add", ".")
	runGitTest(t, "-C", work, "commit", "-m", "update")
	runGitTest(t, "-C", work, "push", "origin", "main")
	return strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
}
