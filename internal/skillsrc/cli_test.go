package skillsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
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
