package skillsrc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncLocalSourceUsesManifestDirectoryAndNeverGitCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifestDir := filepath.Join(root, "config")
	local := filepath.Join(root, "local-skills")
	makeSkill(t, filepath.Join(local, "private-workflow"), "private-workflow", "first")
	manifest := filepath.Join(manifestDir, ".skillsrc")
	writeTestFile(t, manifest, `version=1
[[sources]]
path="../local-skills"
skills=["private-workflow"]
`)
	options := testOptions(root, manifest)

	result, err := NewEngine(options).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Acquisitions != 0 {
		t.Fatalf("local sync acquisitions = %d", result.Acquisitions)
	}
	if _, err := os.Stat(options.CacheDir); !os.IsNotExist(err) {
		t.Fatalf("local source wrote cache %q: %v", options.CacheDir, err)
	}
	installed := filepath.Join(options.TargetDir, "private-workflow", "SKILL.md")
	firstBytes, err := os.ReadFile(installed)
	if err != nil || !strings.Contains(string(firstBytes), "first") {
		t.Fatalf("installed content = %q, error %v", firstBytes, err)
	}
	firstLock, err := LoadLock(options.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstLock.Sources[0].Commit != "" || firstLock.Sources[0].Ref != "" || firstLock.Sources[0].Path != "../local-skills" {
		t.Fatalf("local lock has Git/machine semantics: %#v", firstLock.Sources[0])
	}
	firstHash := firstLock.Sources[0].Skills[0].Hash

	makeSkill(t, filepath.Join(local, "private-workflow"), "private-workflow", "second")
	if _, err := NewEngine(options).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondLock, err := LoadLock(options.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if secondLock.Sources[0].Skills[0].Hash == firstHash {
		t.Fatal("local content change did not update lock hash")
	}
	secondBytes, _ := os.ReadFile(installed)
	if !strings.Contains(string(secondBytes), "second") {
		t.Fatalf("local content change not applied: %s", secondBytes)
	}
}

func TestSyncRefusesUnmanagedCollision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "same"), "same", "managed candidate")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"same\"]\n")
	options := testOptions(root, manifest)
	unmanaged := filepath.Join(options.TargetDir, "same")
	makeSkill(t, unmanaged, "same", "keep me")

	_, err := NewEngine(options).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unmanaged collision") {
		t.Fatalf("Sync() error = %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(unmanaged, "SKILL.md"))
	if !strings.Contains(string(content), "keep me") {
		t.Fatalf("unmanaged content overwritten: %s", content)
	}
}

func TestSyncRepairsManagedDriftAndPrunesOnlyManagedSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	makeSkill(t, filepath.Join(local, "keep"), "keep", "canonical")
	makeSkill(t, filepath.Join(local, "remove"), "remove", "old")
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"keep\",\"remove\"]\n")
	options := testOptions(root, manifest)
	if _, err := NewEngine(options).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(options.TargetDir, "keep", "SKILL.md"), "drifted")
	makeSkill(t, filepath.Join(options.TargetDir, "unmanaged"), "unmanaged", "preserve")
	writeTestFile(t, manifest, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"keep\"]\n")

	if _, err := NewEngine(options).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(options.TargetDir, "keep", "SKILL.md"))
	if !strings.Contains(string(content), "canonical") {
		t.Fatalf("managed drift was not repaired: %s", content)
	}
	if _, err := os.Stat(filepath.Join(options.TargetDir, "remove")); !os.IsNotExist(err) {
		t.Fatalf("removed managed skill still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.TargetDir, "unmanaged", "SKILL.md")); err != nil {
		t.Fatalf("unmanaged skill was pruned: %v", err)
	}
}

func TestSyncGitSourceFetchesOnceForManySkillsThenZero(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote, _ := makeGitRemote(t, map[string]string{
		"skills/one/SKILL.md": "---\nname: one\n---\none\n",
		"skills/two/SKILL.md": "---\nname: two\n---\ntwo\n",
	})
	manifest := filepath.Join(root, ".skillsrc")
	writeTestFile(t, manifest, "version=1\n[[sources]]\nrepo=\""+remote+"\"\nref=\"main\"\nskills=[\"one\",\"two\"]\n")
	options := testOptions(root, manifest)

	first, err := NewEngine(options).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Acquisitions != 1 {
		t.Fatalf("first sync acquisitions = %d", first.Acquisitions)
	}
	second, err := NewEngine(options).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Acquisitions != 0 {
		t.Fatalf("unchanged sync acquisitions = %d", second.Acquisitions)
	}
}

func testOptions(root, manifest string) Options {
	return Options{
		ManifestPath: manifest,
		LockPath:     filepath.Join(filepath.Dir(manifest), "skills.lock"),
		TargetDir:    filepath.Join(root, "target"),
		CacheDir:     filepath.Join(root, "cache", "repos"),
		GitBinary:    "git",
		Out:          &bytes.Buffer{},
		Err:          &bytes.Buffer{},
	}
}
