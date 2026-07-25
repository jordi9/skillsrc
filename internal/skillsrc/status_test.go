package skillsrc

import (
	"context"
	"encoding/json"
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

func TestListAllKeepsDeclaredCollisionAuthoritative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "collision"), "collision", "canonical")
	manifest := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"collision\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(options.TargetDir, "collision")))
	makeSkill(t, filepath.Join(options.TargetDir, "collision"), "collision", "unmanaged")
	writeTestFile(t, filepath.Join(root, "outside"), "outside")
	require.NoError(t, os.Symlink(filepath.Join(root, "outside"), filepath.Join(options.TargetDir, "collision", "unsafe-link")))
	makeSkill(t, filepath.Join(options.TargetDir, "standalone"), "standalone", "unmanaged")

	statuses, err := NewEngine(options).ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, "collision", statuses[0].Name)
	assert.Equal(t, "collision", statuses[0].Status)
	assert.Equal(t, "standalone", statuses[1].Name)
	assert.Equal(t, "unmanaged", statuses[1].Status)
}

func TestListIgnoresLegacyProvenanceInOwnershipMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "canonical")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)

	markerPath := filepath.Join(options.TargetDir, "one", ownershipFile)
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	var marker map[string]any
	require.NoError(t, json.Unmarshal(data, &marker))
	marker["source"] = "legacy-source"
	marker["hash"] = "sha256:legacy"
	data, err = json.Marshal(marker)
	require.NoError(t, err)
	writeTestFile(t, markerPath, string(data)+"\n")

	statuses, err := NewEngine(options).List(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "current", statuses[0].Status)
}

func TestDoctorReportsChangedLocalSourceBeforeSync(t *testing.T) {
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

	report, err := NewEngine(options).Doctor(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, report.Issues, 1)
	assert.Equal(t, "source", report.Issues[0].Kind)
	assert.Equal(t, "one", report.Issues[0].Skill)
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
