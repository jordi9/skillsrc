package skillsrc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncRejectsSkillSymlinkWithoutInstalling(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "safe")
	writeTestFile(t, filepath.Join(root, "outside"), "secret")
	require.NoError(t, os.Symlink(filepath.Join(root, "outside"), filepath.Join(local, "one", "escape")))
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)

	_, err := NewEngine(options).Sync(context.Background())
	require.ErrorContains(t, err, "symlink")
	assert.NoDirExists(t, filepath.Join(options.TargetDir, "one"))
	assert.NoFileExists(t, options.LockPath)
}

func TestSyncRefusesToPruneFormerlyManagedSkillWithoutOwnershipMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "preserve")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)
	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(options.TargetDir, "one", ownershipFile)))
	writeTestFile(t, manifest, "version=1\nsources=[]\n")

	_, err = NewEngine(options).Sync(context.Background())
	require.ErrorContains(t, err, "without valid skillsrc ownership")
	content, readErr := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	require.NoError(t, readErr)
	assert.Contains(t, string(content), "preserve")
}

func TestInstallerRecoversInterruptedReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "skills")
	manifest := filepath.Join(root, ".skillsrc")
	installer := newInstaller(target, manifest)
	require.NoError(t, os.MkdirAll(target, 0o755))
	backup := filepath.Join(target, ".one.skillsrc-old-test")
	staging := filepath.Join(target, ".one.skillsrc-tmp-test")
	makeSkill(t, backup, "one", "old")
	makeSkill(t, staging, "one", "new")
	marker := ownership{Version: SchemaVersion, Owner: installer.owner, Skill: "one", Source: "local:./local", Hash: "sha256:test"}
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	writeTestFile(t, filepath.Join(staging, ownershipFile), string(data)+"\n")
	journal := transaction{Version: SchemaVersion, Action: "replace", Skill: "one", Temp: filepath.Base(staging), Backup: filepath.Base(backup)}
	require.NoError(t, writeJSONAtomic(filepath.Join(target, ".one.skillsrc-txn.json"), journal))

	require.NoError(t, installer.withLock(context.Background(), func() error { return nil }))
	content, err := os.ReadFile(filepath.Join(target, "one", "SKILL.md"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(content), "new"))
	assert.NoDirExists(t, backup)
	assert.NoFileExists(t, filepath.Join(target, ".one.skillsrc-txn.json"))
}
