package skillsrc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListReportsCurrentMissingDriftedAndCollision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	for _, name := range []string{"current", "missing", "drifted", "collision"} {
		makeSkill(t, filepath.Join(local, name), name, "canonical")
	}
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"current\",\"missing\",\"drifted\",\"collision\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(options.TargetDir, "missing")))
	writeTestFile(t, filepath.Join(options.TargetDir, "drifted", "SKILL.md"), "changed")
	require.NoError(t, os.RemoveAll(filepath.Join(options.TargetDir, "collision")))
	makeSkill(t, filepath.Join(options.TargetDir, "collision"), "collision", "unmanaged")

	statuses, err := NewEngine(options).List(context.Background())
	require.NoError(t, err)
	got := make(map[string]string)
	for _, status := range statuses {
		got[status.Name] = status.Status
	}
	assert.Equal(t, "current", got["current"])
	assert.Equal(t, "missing", got["missing"])
	assert.Equal(t, "drifted", got["drifted"])
	assert.Equal(t, "collision", got["collision"])
}

func TestDoctorReportsWithoutRepairAndRepairsOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "canonical")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	writeTestFile(t, filepath.Join(options.TargetDir, "one", "SKILL.md"), "drifted")

	report, err := NewEngine(options).Doctor(context.Background(), false)
	require.NoError(t, err)
	require.NotEmpty(t, report.Issues)
	statuses, err := NewEngine(options).List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "drifted", statuses[0].Status)

	report, err = NewEngine(options).Doctor(context.Background(), true)
	require.NoError(t, err)
	assert.Empty(t, report.Issues)
	statuses, err = NewEngine(options).List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "current", statuses[0].Status)
}
