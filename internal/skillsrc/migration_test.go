package skillsrc

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsolatedMigrationConfiguration(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "migration", ".skillsrc")
	fixture, err := LoadManifest(fixturePath)
	require.NoError(t, err)
	require.Len(t, fixture.Sources, 5)
	total := 0
	for _, source := range fixture.Sources {
		total += len(source.Skills)
	}
	assert.Equal(t, 26, total, "the supplied live manifest contains 26 entries")
	require.Len(t, fixture.Sources[0].Skills, 19)

	root := t.TempDir()
	remotes := make([]string, len(fixture.Sources))
	for i, source := range fixture.Sources {
		files := make(map[string]string, len(source.Skills))
		for _, name := range source.Skills {
			files[filepath.ToSlash(filepath.Join("skills", name, "SKILL.md"))] = fmt.Sprintf("---\nname: %s\n---\nfixture\n", name)
		}
		remotes[i], _ = makeGitRemote(t, files)
	}

	jordiManifest := filepath.Join(root, "jordi", ".skillsrc")
	writeMigrationManifest(t, jordiManifest, remotes[:1], fixture.Sources[:1])
	jordiOptions := testOptions(filepath.Join(root, "jordi-run"), jordiManifest)
	started := time.Now()
	jordiResult, err := NewEngine(jordiOptions).Sync(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, jordiResult.Acquisitions, "19 skills from one repository must acquire once")
	t.Logf("19-skill source: acquisitions=%d elapsed=%s", jordiResult.Acquisitions, time.Since(started))

	manifest := filepath.Join(root, "full", ".skillsrc")
	writeMigrationManifest(t, manifest, remotes, fixture.Sources)
	options := testOptions(filepath.Join(root, "full-run"), manifest)
	started = time.Now()
	first, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 5, first.Acquisitions)
	firstElapsed := time.Since(started)

	started = time.Now()
	unchanged, err := NewEngine(options).Sync(context.Background())
	require.NoError(t, err)
	assert.Zero(t, unchanged.Acquisitions)
	unchangedElapsed := time.Since(started)

	started = time.Now()
	updated, err := NewEngine(options).Update(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 5, updated.Acquisitions)
	updateElapsed := time.Since(started)

	statuses, err := NewEngine(options).List(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 26)
	for _, status := range statuses {
		assert.Equal(t, "current", status.Status, status.Name)
	}
	report, err := NewEngine(options).Doctor(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, report.Issues)
	t.Logf("full 26-skill isolated run: sync acquisitions=%d elapsed=%s; unchanged acquisitions=%d elapsed=%s; update acquisitions=%d elapsed=%s", first.Acquisitions, firstElapsed, unchanged.Acquisitions, unchangedElapsed, updated.Acquisitions, updateElapsed)
}

func writeMigrationManifest(t *testing.T, path string, remotes []string, sources []ManifestSource) {
	t.Helper()
	var body strings.Builder
	body.WriteString("version = 1\n")
	for i, source := range sources {
		body.WriteString("\n[[sources]]\nrepo = ")
		body.WriteString(fmt.Sprintf("%q\n", remotes[i]))
		body.WriteString("skills = [")
		for j, name := range source.Skills {
			if j > 0 {
				body.WriteString(", ")
			}
			body.WriteString(fmt.Sprintf("%q", name))
		}
		body.WriteString("]\n")
	}
	writeTestFile(t, path, body.String())
}
