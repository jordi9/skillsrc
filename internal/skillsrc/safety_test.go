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

func TestSyncDereferencesSafeInternalFileSymlink(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "local")
	skill := filepath.Join(local, "one")
	makeSkill(t, skill, "one", "safe")
	writeTestFile(t, filepath.Join(skill, "AGENTS.md"), "instructions")
	require.NoError(t, os.Symlink("AGENTS.md", filepath.Join(skill, "CLAUDE.md")))
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)

	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	installed := filepath.Join(options.TargetDir, "one", "CLAUDE.md")
	info, err := os.Lstat(installed)
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink)
	content, err := os.ReadFile(installed)
	require.NoError(t, err)
	assert.Equal(t, "instructions", string(content))
}

func TestSyncRejectsSymlinkedLocalSourceRoot(t *testing.T) {
	root := t.TempDir()
	realSource := filepath.Join(root, "real")
	makeSkill(t, filepath.Join(realSource, "one"), "one", "safe")
	require.NoError(t, os.Symlink(realSource, filepath.Join(root, "linked")))
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./linked\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)

	_, err := NewEngine(options).Sync(context.Background())
	require.ErrorContains(t, err, "not a real directory")
	assert.NoDirExists(t, filepath.Join(options.TargetDir, "one"))
}

func TestSyncWritesOwnershipAndInstallHashesToManagedMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "one"), "one", "safe")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	options := testOptions(root, manifest)

	_, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(options.TargetDir, "one", ownershipFile))
	require.NoError(t, err)
	var marker map[string]any
	require.NoError(t, json.Unmarshal(data, &marker))
	assert.Equal(t, float64(SchemaVersion), marker["version"])
	assert.Equal(t, newInstaller(options.TargetDir, manifest, filepath.Join(root, "locks")).owner, marker["owner"])
	assert.Equal(t, "one", marker["skill"])
	assert.NotEmpty(t, marker["source_hash"])
	assert.NotEmpty(t, marker["installed_hash"])
	assert.Equal(t, false, marker["disable_model_invocation"])
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

func TestInstallerRejectsEscapingTransactionPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "skills")
	victim := filepath.Join(root, "victim")
	writeTestFile(t, filepath.Join(victim, "keep"), "preserve")
	require.NoError(t, os.MkdirAll(target, 0o755))
	journal := transaction{Version: SchemaVersion, Action: "prune", Skill: "one", Backup: "../victim"}
	require.NoError(t, writeJSONAtomic(filepath.Join(target, ".one.skillsrc-txn.json"), journal))
	installer := newInstaller(target, filepath.Join(root, ".skillsrc"), filepath.Join(root, "locks"))

	err := installer.withLock(context.Background(), func() error { return nil })
	require.ErrorContains(t, err, "invalid transaction paths")
	assert.FileExists(t, filepath.Join(victim, "keep"))
}

func TestGeneratedArtifactParsesSkillNamesContainingMarkerText(t *testing.T) {
	t.Parallel()
	name := ".foo.skillsrc-tmp-bar.skillsrc-tmp-123"
	skill, ok := generatedArtifact(name, "tmp")
	assert.True(t, ok)
	assert.Equal(t, "foo.skillsrc-tmp-bar", skill)
}

func TestInstallerPreservesUnownedArtifactLikeDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "skills")
	artifact := filepath.Join(target, ".foo.skillsrc-tmp-user")
	writeTestFile(t, filepath.Join(artifact, "keep"), "preserve")
	installer := newInstaller(target, filepath.Join(root, ".skillsrc"), filepath.Join(root, "locks"))

	err := installer.withLock(context.Background(), func() error { return nil })
	require.ErrorContains(t, err, "requires inspection")
	assert.FileExists(t, filepath.Join(artifact, "keep"))
}

func TestInstallerRecoversInterruptedReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "skills")
	manifest := filepath.Join(root, ".skillsrc")
	installer := newInstaller(target, manifest, filepath.Join(root, "locks"))
	require.NoError(t, os.MkdirAll(target, 0o755))
	backup := filepath.Join(target, ".one.skillsrc-old-test")
	staging := filepath.Join(target, ".one.skillsrc-tmp-test")
	makeSkill(t, backup, "one", "old")
	makeSkill(t, staging, "one", "new")
	marker := ownership{Version: SchemaVersion, Owner: installer.owner, Skill: "one"}
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
