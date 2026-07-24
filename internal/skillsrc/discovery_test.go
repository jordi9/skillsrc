package skillsrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSkillsUsesOnlyConventionalLocations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeSkill(t, filepath.Join(root, "direct"), "direct", "one")
	makeSkill(t, filepath.Join(root, "skills", "nested"), "nested", "two")
	makeSkill(t, filepath.Join(root, "skills", "category", "categorized"), "categorized", "two levels")
	makeSkill(t, filepath.Join(root, "skills", "category", "too", "deep"), "deep", "ignored")
	makeSkill(t, filepath.Join(root, ".agents", "skills", "agent"), "agent", "three")
	makeSkill(t, filepath.Join(root, ".claude", "skills", "claude"), "claude", "four")
	makeSkill(t, filepath.Join(root, "deep", "ignored", "skill"), "ignored", "no")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"direct", "nested", "categorized", "agent", "claude"} {
		if _, ok := found[name]; !ok {
			t.Errorf("skill %q not discovered: %#v", name, found)
		}
	}
	if _, ok := found["ignored"]; ok {
		t.Fatalf("unrestricted recursive skill was discovered: %#v", found)
	}
	if _, ok := found["deep"]; ok {
		t.Fatalf("skill below bounded category depth was discovered: %#v", found)
	}
}

func TestDiscoverSkillsRejectsAmbiguousNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeSkill(t, filepath.Join(root, "same"), "same", "one")
	makeSkill(t, filepath.Join(root, "skills", "same"), "same", "two")

	_, err := DiscoverSkills(root)
	if err == nil || !strings.Contains(err.Error(), `ambiguous skill "same"`) {
		t.Fatalf("DiscoverSkills() error = %v", err)
	}
}

func TestHashSkillRejectsSymlinksAndIsStable(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "safe")
	makeSkill(t, skill, "safe", "body")
	writeTestFile(t, filepath.Join(skill, "references", "guide.md"), "guide")

	first, err := HashSkill(skill)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashSkill(skill)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("unstable/invalid hashes %q and %q", first, second)
	}

	outside := filepath.Join(root, "outside")
	writeTestFile(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(skill, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := HashSkill(skill); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("HashSkill() error = %v", err)
	}
}

func TestValidateRelativeSkillPathRejectsEscape(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../skill", "/tmp/skill", "skills/../../skill", "."} {
		if err := ValidateRelativeSkillPath(path); err == nil {
			t.Errorf("ValidateRelativeSkillPath(%q) succeeded", path)
		}
	}
	if err := ValidateRelativeSkillPath("skills/good"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func makeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\n---\n"+body+"\n")
}
