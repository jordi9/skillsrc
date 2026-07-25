package skillsrc

import (
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
	if len(result.Fetches) != 0 {
		t.Fatalf("local sync fetches = %d", len(result.Fetches))
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

func TestSyncDerivesDisableModelInvocationInstallWithoutChangingSourceOrLockHash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "local", "one")
	writeTestFile(t, filepath.Join(local, "SKILL.md"), "---\nname: one\ndisable-model-invocation: false\n---\nbody\n")
	sourceBefore, err := os.ReadFile(filepath.Join(local, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	upstreamHash, err := HashSkill(local)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"!one\"]\n")
	options := testOptions(root, manifestPath)
	if _, err := NewEngine(options).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "disable-model-invocation: true") {
		t.Fatalf("installed SKILL.md missing override:\n%s", installed)
	}
	sourceAfter, _ := os.ReadFile(filepath.Join(local, "SKILL.md"))
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("source SKILL.md was mutated")
	}
	lock, err := LoadLock(options.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := lock.Sources[0].Skills[0].Hash; got != upstreamHash {
		t.Fatalf("lock hash = %q, want upstream hash %q", got, upstreamHash)
	}

	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"one\"]\n")
	statuses, err := NewEngine(options).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Status != "drifted" {
		t.Fatalf("status after option change = %q, want drifted", statuses[0].Status)
	}
	if _, err := NewEngine(options).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	installed, _ = os.ReadFile(filepath.Join(options.TargetDir, "one", "SKILL.md"))
	if string(installed) != string(sourceBefore) {
		t.Fatalf("option change was not applied:\n%s", installed)
	}
}

func TestSyncDisableModelInvocationRequiresClosingFrontmatter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "local", "one", "SKILL.md"), "---\nname: one\n")
	manifestPath := filepath.Join(root, "skills.toml")
	writeTestFile(t, manifestPath, "version=1\n[[sources]]\npath=\"./local\"\nskills=[\"!one\"]\n")
	_, err := NewEngine(testOptions(root, manifestPath)).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closing YAML frontmatter delimiter") {
		t.Fatalf("Sync() error = %v", err)
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

	result, err := NewEngine(options).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keepRestored := false
	for _, action := range result.Skills {
		if action == (SkillAction{Name: "keep", Action: "repaired"}) {
			keepRestored = true
		}
	}
	if !keepRestored {
		t.Fatalf("sync actions = %#v, want keep repaired", result.Skills)
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
	if len(first.Fetches) != 1 {
		t.Fatalf("first sync fetches = %d", len(first.Fetches))
	}
	second, err := NewEngine(options).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Fetches) != 0 {
		t.Fatalf("unchanged sync fetches = %d", len(second.Fetches))
	}
}

func testOptions(root, manifest string) Options {
	return Options{
		ManifestPath: manifest,
		LockPath:     filepath.Join(filepath.Dir(manifest), "skills.lock"),
		TargetDir:    filepath.Join(root, "target"),
		CacheDir:     filepath.Join(root, "cache", "repos"),
		GitBinary:    "git",
	}
}
