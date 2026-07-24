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
