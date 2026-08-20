package skillsrc

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestDiscoverSkillsUsesClaudeMarketplacePluginLocations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{
		"metadata": {"pluginRoot": "./plugins"},
		"plugins": [
			{"name": "one", "source": "./one"},
			{"name": "two", "source": "./two", "skills": ["./custom/explicit"]}
		]
	}`)
	makeSkill(t, filepath.Join(root, "plugins", "one", "skills", "conventional"), "conventional", "one")
	makeSkill(t, filepath.Join(root, "plugins", "two", "custom", "explicit"), "explicit", "two")
	makeSkill(t, filepath.Join(root, "plugins", "unlisted", "skills", "ignored"), "ignored", "no")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, wantPath := range map[string]string{
		"conventional": "plugins/one/skills/conventional",
		"explicit":     "plugins/two/custom/explicit",
	} {
		if got := found[name].Path; got != wantPath {
			t.Errorf("skill %q path = %q, want %q", name, got, wantPath)
		}
	}
	if _, ok := found["ignored"]; ok {
		t.Fatalf("skill from undeclared plugin was discovered: %#v", found)
	}
}

func TestDiscoverSkillsUsesDirectorySlugForInvalidMarketplaceName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{"plugins":[{"name":"pstack","source":"pstack"}]}`)
	writeTestFile(t, filepath.Join(root, "pstack", ".cursor-plugin", "plugin.json"), `{"name":"pstack","skills":"./skills/"}`)
	makeSkill(t, filepath.Join(root, "pstack", "skills", "poteto-mode"), "Poteto Mode", "body")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["poteto-mode"].Path; got != "pstack/skills/poteto-mode" {
		t.Fatalf("poteto-mode path = %q", got)
	}
}

func TestDiscoverSkillsUsesCursorMarketplaceAndDeclaredDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{
		"plugins": [{"name": "pstack", "source": "pstack"}]
	}`)
	writeTestFile(t, filepath.Join(root, "pstack", ".cursor-plugin", "plugin.json"), `{
		"name": "pstack", "skills": "./skill-library/"
	}`)
	makeSkill(t, filepath.Join(root, "pstack", "skill-library", "review"), "review", "cursor")
	makeSkill(t, filepath.Join(root, "pstack", "skill-library", "group", "nested"), "nested", "ignored")
	makeSkill(t, filepath.Join(root, "pstack", "skills", "undeclared"), "undeclared", "ignored")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["review"].Path; got != "pstack/skill-library/review" {
		t.Fatalf("review path = %q", got)
	}
	if _, ok := found["nested"]; ok {
		t.Fatal("nested skill in a declared directory was discovered")
	}
	if _, ok := found["undeclared"]; ok {
		t.Fatal("conventional skills were used despite an explicit Cursor declaration")
	}
}

func TestDiscoverSkillsIgnoresCursorMarketplaceEntryWithoutManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{"plugins":[{"name":"missing-manifest","source":"plugin"}]}`)
	makeSkill(t, filepath.Join(root, "plugin", "skills", "ignored"), "ignored", "body")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := found["ignored"]; ok {
		t.Fatalf("skill from entry without manifest was discovered: %#v", found)
	}
}

func TestDiscoverSkillsUsesRootCursorPluginManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "plugin.json"), `{"name":"root-plugin","skills":"./skills/"}`)
	makeSkill(t, filepath.Join(root, "skills", "root-skill"), "root-skill", "body")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["root-skill"].Path; got != "skills/root-skill" {
		t.Fatalf("root-skill path = %q", got)
	}
}

func TestDiscoverSkillsCursorSupportsStringArraysAndSafeBareSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), fmt.Sprintf(`{
		"plugins": [
			{"name":"bare", "source":"bare"},
			{"name":"dot", "source":"./dot"},
			{"name":"absolute", "source":%q},
			{"name":"escape", "source":"../escape"},
			{"name":"remote", "source":"https://example.com/plugin"},
			{"name":"object", "source":{"url":"remote"}}
		]
	}`, filepath.ToSlash(outside)))
	for _, plugin := range []string{"bare", "dot"} {
		writeTestFile(t, filepath.Join(root, plugin, ".cursor-plugin", "plugin.json"), `{"skills":["./one", "./two"]}`)
		makeSkill(t, filepath.Join(root, plugin, "one", "first"), plugin+"-first", "one")
		makeSkill(t, filepath.Join(root, plugin, "two", "second"), plugin+"-second", "two")
	}
	makeSkill(t, filepath.Join(outside, "skills", "unsafe"), "unsafe", "outside")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bare-first", "bare-second", "dot-first", "dot-second"} {
		if _, ok := found[name]; !ok {
			t.Errorf("declared skill %q was not discovered: %#v", name, found)
		}
	}
	if _, ok := found["unsafe"]; ok {
		t.Fatalf("unsafe marketplace source was discovered: %#v", found)
	}
}

func TestDiscoverSkillsKeepsFirstConflictingMarketplaceSkillAndWarns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{
		"plugins":[{"name":"one","source":"one"},{"name":"two","source":"two"}]
	}`)
	for _, plugin := range []string{"one", "two"} {
		writeTestFile(t, filepath.Join(root, plugin, ".cursor-plugin", "plugin.json"), `{}`)
		makeSkill(t, filepath.Join(root, plugin, "skills", "shared"), "shared", plugin)
	}

	found, warnings, err := DiscoverSkillsWithWarnings(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["shared"].Path; got != "one/skills/shared" {
		t.Fatalf("shared path = %q, want first discovered copy", got)
	}
	if got, want := warnings, []string{`duplicate skill "shared"; using "one/skills/shared" and ignoring "two/skills/shared"`}; !slices.Equal(got, want) {
		t.Fatalf("warnings = %#v, want %#v", got, want)
	}
}

func TestDiscoverSkillsDoesNotUsePackagingNamesToResolveConflicts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{
		"plugins":[{"name":"bundle","source":"bundle"},{"name":"shared","source":"dedicated"}]
	}`)
	for _, plugin := range []string{"bundle", "dedicated"} {
		writeTestFile(t, filepath.Join(root, plugin, ".cursor-plugin", "plugin.json"), `{}`)
		makeSkill(t, filepath.Join(root, plugin, "skills", "shared"), "shared", plugin)
	}

	found, warnings, err := DiscoverSkillsWithWarnings(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["shared"].Path; got != "bundle/skills/shared" {
		t.Fatalf("shared path = %q, want first discovered copy", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `ignoring "dedicated/skills/shared"`) {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestDiscoverSkillsCollapsesIdenticalMarketplaceDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".cursor-plugin", "marketplace.json"), `{
		"plugins":[{"name":"one","source":"one"},{"name":"two","source":"two"}]
	}`)
	for _, plugin := range []string{"one", "two"} {
		writeTestFile(t, filepath.Join(root, plugin, ".cursor-plugin", "plugin.json"), `{}`)
		makeSkill(t, filepath.Join(root, plugin, "skills", "shared"), "shared", "identical")
	}

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["shared"].Path; got != "one/skills/shared" {
		t.Fatalf("collapsed shared path = %q", got)
	}
}

func TestDiscoverSkillsUsesRootClaudePluginManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), `{
		"name": "root-plugin",
		"skills": ["./custom/root-skill"]
	}`)
	makeSkill(t, filepath.Join(root, "custom", "root-skill"), "root-skill", "one")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["root-skill"].Path; got != "custom/root-skill" {
		t.Fatalf("root plugin skill path = %q, want custom/root-skill", got)
	}
}

func TestDiscoverSkillsIgnoresUnsafeClaudePluginPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	makeSkill(t, filepath.Join(outside, "escaped"), "escaped", "no")
	writeTestFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), fmt.Sprintf(`{
		"plugins": [
			{"source": %q},
			{"source": "plugin", "skills": ["./skills/bare-source"]},
			{"source": "./safe", "skills": ["../escaped"]}
		]
	}`, "./"+filepath.ToSlash(filepath.Join("..", filepath.Base(outside)))))
	makeSkill(t, filepath.Join(root, "plugin", "skills", "bare-source"), "bare-source", "no")
	makeSkill(t, filepath.Join(root, "safe", "escaped"), "unsafe-skill-path", "no")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("unsafe manifest paths were discovered: %#v", found)
	}
}

func TestDiscoverSkillsUsesConventionalLocationPrecedenceForDuplicateNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeSkill(t, filepath.Join(root, "same"), "same", "direct")
	makeSkill(t, filepath.Join(root, "skills", "same"), "same", "skills")
	makeSkill(t, filepath.Join(root, ".agents", "skills", "same"), "same", "agents")
	makeSkill(t, filepath.Join(root, ".claude", "skills", "same"), "same", "claude")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["same"].Path; got != "same" {
		t.Fatalf("duplicate skill path = %q, want direct skill path", got)
	}
}

func TestDiscoverSkillsRejectsDuplicateNamesAtSamePriority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		paths []string
	}{
		{name: "direct children", paths: []string{"one", "two"}},
		{name: "root and direct child", paths: []string{".", "one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for index, path := range test.paths {
				makeSkill(t, filepath.Join(root, path), "same", fmt.Sprintf("variant %d", index))
			}

			_, err := DiscoverSkills(root)
			if err == nil || !strings.Contains(err.Error(), `ambiguous skill "same"`) {
				t.Fatalf("DiscoverSkills() error = %v", err)
			}
		})
	}
}

func TestDiscoverSkillsPrefersAgentsSkillOverClaudeSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeSkill(t, filepath.Join(root, ".agents", "skills", "impeccable"), "impeccable", "agents")
	makeSkill(t, filepath.Join(root, ".claude", "skills", "impeccable"), "impeccable", "claude")

	found, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := found["impeccable"].Path; got != ".agents/skills/impeccable" {
		t.Fatalf("duplicate skill path = %q, want .agents skill path", got)
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
