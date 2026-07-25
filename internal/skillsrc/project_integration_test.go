package skillsrc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorReportsAndRepairsProjectMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version = 1\n[[sources]]\npath = \"./local\"\nskills = [\"one\"]\n")
	options := testOptions(root, manifest)
	options.ProjectRoot = root
	writeTestFile(t, filepath.Join(root, ".gitignore"), "# skillsrc runtime\n/.skillsrc-install.lock\n/.agents/.gitignore\n")
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(root, ".skillsrc-install.lock"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(options.TargetDir), ".skillsrc-install.lock"))
	rootMetadata, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(rootMetadata), ".skillsrc-install.lock")
	lockEntries, err := os.ReadDir(filepath.Join(filepath.Dir(options.CacheDir), "locks"))
	require.NoError(t, err)
	assert.Len(t, lockEntries, 2)

	require.NoError(t, os.Remove(filepath.Join(root, ".gitignore")))
	writeTestFile(t, filepath.Join(root, ".agents", ".gitignore"), "stale\n")
	report, err := NewEngine(options).Doctor(context.Background(), false)
	require.NoError(t, err)
	var projectMessages []string
	for _, issue := range report.Issues {
		if issue.Kind == "project" {
			projectMessages = append(projectMessages, issue.Message)
		}
	}
	assert.ElementsMatch(t, []string{"root .gitignore is missing skillsrc runtime entries", ".agents/.gitignore is stale"}, projectMessages)

	report, err = NewEngine(options).Doctor(context.Background(), true)
	require.NoError(t, err)
	assert.Empty(t, report.Issues)
	rootIgnore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(rootIgnore), "/.agents/.gitignore\n")
	assert.NotContains(t, string(rootIgnore), ".skillsrc-install.lock")
	managed, err := os.ReadFile(filepath.Join(root, ".agents", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(managed), "/skills/one/\n")
}

func TestFailedProjectMutationLeavesManagedGitignoreUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version = 1\n[[sources]]\npath = \"./local\"\nskills = [\"one\"]\n")
	options := testOptions(root, manifest)
	options.ProjectRoot = root
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	ignorePath := filepath.Join(root, ".agents", ".gitignore")
	before, err := os.ReadFile(ignorePath)
	require.NoError(t, err)

	writeTestFile(t, manifest, "version = 1\n[[sources]]\npath = \"./local\"\nskills = [\"missing\"]\n")
	_, err = NewEngine(options).Sync(context.Background())
	require.Error(t, err)
	after, err := os.ReadFile(ignorePath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestRemoveRefreshesManagedGitignoreWithoutHidingUnmanagedSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "body")
	makeSkill(t, filepath.Join(local, "two"), "two", "body")
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version = 1\n[[sources]]\npath = \"./local\"\nskills = [\"one\", \"two\"]\n")
	options := testOptions(root, manifest)
	options.ProjectRoot = root
	engine := NewEngine(options)
	_, err := engine.Sync(context.Background())
	require.NoError(t, err)
	_, err = engine.Remove(context.Background(), []string{"two"})
	require.NoError(t, err)

	managed, err := os.ReadFile(filepath.Join(root, ".agents", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(managed), "/skills/one/\n")
	assert.NotContains(t, string(managed), "/skills/two/\n")
	assert.NotContains(t, string(managed), "/skills/\n")
}
