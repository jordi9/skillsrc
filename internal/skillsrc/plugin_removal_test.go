package skillsrc

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverTreatsMarketplacePluginsAsOrdinarySkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeCursorPluginMarketplace(t, local, "pstack")
	makeSkill(t, filepath.Join(local, "pstack", "skills", "architect"), "architect", "architecture")
	makeSkill(t, filepath.Join(local, "pstack", "skills", "arena"), "arena", "parallel")
	makeSkill(t, filepath.Join(local, "standalone"), "standalone", "body")
	var output, errorOutput bytes.Buffer
	runtime := CLIOptions{WorkingDir: root, HomeDir: root, CacheDir: filepath.Join(root, "cache"), GitBinary: "git", Out: &output, Err: &errorOutput}

	require.Equal(t, 0, RunCLI(context.Background(), []string{"discover", local}, runtime), errorOutput.String())
	assert.Equal(t, "Available skills from "+displaySource(local, root)+":\n  • architect\n  • arena\n  • standalone\n", output.String())
	assert.NoFileExists(t, filepath.Join(root, "skills.toml"))
	assert.NoFileExists(t, filepath.Join(root, "skills.lock"))
	assert.NoDirExists(t, filepath.Join(root, ".agents"))
}

func TestPluginFlagsAreNotPartOfTheCLI(t *testing.T) {
	t.Parallel()

	_, err := parseAddArgs([]string{"owner/repository", "--plugin", "bundle"})
	assert.EqualError(t, err, `unknown add option "--plugin"`)

	_, err = parseDiscoverArgs([]string{"owner/repository", "--plugin", "bundle"})
	assert.EqualError(t, err, `unknown discover option "--plugin"`)

	root := t.TempDir()
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer
	assert.Equal(t, 1, runCLIResolved(context.Background(), []string{"remove", "--plugin", "bundle"}, options, &output, &errorOutput))
	assert.Contains(t, errorOutput.String(), `unknown remove option "--plugin"`)
}

func makeCursorPluginMarketplace(t *testing.T, root, plugin string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), fmt.Sprintf(`{"plugins":[{"name":%q,"source":%q}]}`, plugin, plugin))
	writeTestFile(t, filepath.Join(root, plugin, ".cursor-plugin", "plugin.json"), fmt.Sprintf(`{"name":%q,"skills":"./skills/"}`, plugin))
}

func TestDiscoverWarnsAndKeepsFirstConflictingMarketplaceSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	writeTestFile(t, filepath.Join(local, ".cursor-plugin", "marketplace.json"), `{"plugins":[{"source":"one"},{"source":"two"}]}`)
	for _, source := range []string{"one", "two"} {
		writeTestFile(t, filepath.Join(local, source, ".cursor-plugin", "plugin.json"), `{}`)
		makeSkill(t, filepath.Join(local, source, "skills", "shared"), "shared", source)
	}
	var output, errorOutput bytes.Buffer
	runtime := CLIOptions{WorkingDir: root, HomeDir: root, CacheDir: filepath.Join(root, "cache"), GitBinary: "git", Out: &output, Err: &errorOutput}

	require.Equal(t, 0, RunCLI(context.Background(), []string{"discover", local}, runtime), errorOutput.String())
	assert.Contains(t, output.String(), `! Warning: duplicate skill "shared"; using "one/skills/shared" and ignoring "two/skills/shared"`)
	assert.Contains(t, output.String(), "Available skills from "+displaySource(local, root)+":\n  • shared\n")
}

func TestAddWarnsAndLocksFirstConflictingMarketplaceSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	writeTestFile(t, filepath.Join(local, ".cursor-plugin", "marketplace.json"), `{"plugins":[{"source":"one"},{"source":"two"}]}`)
	for _, source := range []string{"one", "two"} {
		writeTestFile(t, filepath.Join(local, source, ".cursor-plugin", "plugin.json"), `{}`)
		makeSkill(t, filepath.Join(local, source, "skills", "shared"), "shared", source)
	}
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer

	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", local, "shared"}, options, &output, &errorOutput), errorOutput.String())
	assert.Contains(t, output.String(), `! Warning: duplicate skill "shared"; using "one/skills/shared" and ignoring "two/skills/shared"`)
	lock, err := LoadLock(options.LockPath)
	require.NoError(t, err)
	require.Len(t, lock.Sources, 1)
	require.Len(t, lock.Sources[0].Skills, 1)
	assert.Equal(t, "one/skills/shared", lock.Sources[0].Skills[0].Path)

	output.Reset()
	errorOutput.Reset()
	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"sync"}, options, &output, &errorOutput), errorOutput.String())
	assert.NotContains(t, output.String(), "Warning:")
}

func TestAddSelectsSkillDiscoveredThroughMarketplaceMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "source")
	makeCursorPluginMarketplace(t, local, "pstack")
	makeSkill(t, filepath.Join(local, "pstack", "skills", "architect"), "architect", "architecture")
	makeSkill(t, filepath.Join(local, "pstack", "skills", "arena"), "arena", "parallel")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version = 1\n")
	options := testOptions(root, manifestPath)
	var output, errorOutput bytes.Buffer

	require.Equal(t, 0, runCLIResolved(context.Background(), []string{"add", local, "architect"}, options, &output, &errorOutput), errorOutput.String())
	manifest, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, []string{"architect"}, manifest.Sources[0].Skills)
	assert.FileExists(t, filepath.Join(options.TargetDir, "architect", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(options.TargetDir, "arena"))
}
