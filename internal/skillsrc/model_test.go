package skillsrc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadManifestValidatesSourcesAndNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"both repo and path", `version=1
[[sources]]
repo="owner/repo"
path="./skills"
skills=["one"]
`, "exactly one"},
		{"neither repo nor path", `version=1
[[sources]]
skills=["one"]
`, "exactly one"},
		{"duplicate selected skill", `version=1
[[sources]]
repo="owner/one"
skills=["same"]
[[sources]]
repo="owner/two"
skills=["same"]
`, `duplicate skill "same"`},
		{"local ref", `version=1
[[sources]]
path="./skills"
ref="main"
skills=["one"]
`, "local source cannot set ref"},
		{"escaping skill name", `version=1
[[sources]]
path="./skills"
skills=["../one"]
`, "invalid skill name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".toml")
			writeTestFile(t, path, tt.body)
			_, err := LoadManifest(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadManifest() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadManifestRejectsPluginAndEmptySkillSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"plugin key", "version=1\n[[sources]]\nrepo=\"cursor/plugins\"\nplugin=\"pstack\"\nskills=[\"one\"]\n", "unknown manifest key"},
		{"empty skills", "version=1\n[[sources]]\nrepo=\"owner/repo\"\n", "must select at least one skill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skills.toml")
			writeTestFile(t, path, tt.body)
			_, err := LoadManifest(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadManifest() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadLockRejectsPluginKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "skills.lock")
	writeTestFile(t, path, `version = 1
[[sources]]
kind = "git"
identity = "github.com/owner/repo"
repo = "owner/repo"
plugin = "pstack"
commit = "0123456789abcdef0123456789abcdef01234567"
[[sources.skills]]
name = "one"
path = "skills/one"
hash = "sha256:one"
`)
	_, err := LoadLock(path)
	if err == nil || !strings.Contains(err.Error(), "unknown lock key") {
		t.Fatalf("LoadLock() error = %v, want unknown lock key", err)
	}
}

func TestLoadManifestAcceptsDisableModelInvocationForms(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "skills.toml")
	writeTestFile(t, path, `version = 1
[[sources]]
path = "./local"
skills = ["plain", "!shortcut", { name = "object", disable-model-invocation = true }, { name = "false", disable-model-invocation = false }]
`)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	source := manifest.Sources[0]
	if got, want := source.Skills, []string{"plain", "shortcut", "object", "false"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Skills = %v, want %v", got, want)
	}
	if !source.DisableModelInvocation["shortcut"] || !source.DisableModelInvocation["object"] || source.DisableModelInvocation["plain"] || source.DisableModelInvocation["false"] {
		t.Fatalf("DisableModelInvocation = %#v", source.DisableModelInvocation)
	}
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"!shortcut"`) || !strings.Contains(string(encoded), `"!object"`) {
		t.Fatalf("encoded manifest does not preserve overrides:\n%s", encoded)
	}
}

func TestLoadManifestRejectsMalformedDisableModelInvocationEntries(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, entry, want string }{
		{"empty shortcut", `"!"`, "invalid skill name"},
		{"duplicate logical name", `"one", "!one"`, `duplicate skill "one"`},
		{"unknown object key", `{ name = "one", other = true }`, "unknown skill key"},
		{"missing object name", `{ disable-model-invocation = true }`, "string name"},
		{"invalid option type", `{ name = "one", disable-model-invocation = "yes" }`, "must be a boolean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skills.toml")
			writeTestFile(t, path, "version=1\n[[sources]]\npath=\"./local\"\nskills=["+tt.entry+"]\n")
			_, err := LoadManifest(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadManifest() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadManifestResolvesLocalPathsFromManifestDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "config", ".skillsrc")
	writeTestFile(t, manifestPath, `version=1
[[sources]]
path="../local-skills"
skills=["one"]
`)

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "local-skills")
	if got := manifest.Sources[0].ResolvedPath; got != want {
		t.Fatalf("ResolvedPath = %q, want %q", got, want)
	}
	if got := manifest.Sources[0].Path; got != "../local-skills" {
		t.Fatalf("Path was rewritten to %q", got)
	}
}

func TestEncodeManifestIsDeterministicAndReloadable(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		Version: 1,
		Path:    "/runtime/manifest",
		Sources: []ManifestSource{
			{
				Repo:         "z/repo",
				Ref:          "main",
				Skills:       []string{"zeta", "alpha"},
				ResolvedPath: "/runtime/git",
			},
			{
				Path:         "../local",
				Skills:       []string{"gamma", "beta"},
				ResolvedPath: "/runtime/local",
			},
		},
	}
	before := Manifest{
		Version: manifest.Version,
		Path:    manifest.Path,
		Sources: append([]ManifestSource(nil), manifest.Sources...),
	}
	for i := range before.Sources {
		before.Sources[i].Skills = append([]string(nil), manifest.Sources[i].Skills...)
	}

	first, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifest output changed:\n%s\n---\n%s", first, second)
	}
	if !reflect.DeepEqual(manifest, before) {
		t.Fatalf("EncodeManifest mutated input:\ngot:  %#v\nwant: %#v", manifest, before)
	}

	output := string(first)
	if strings.Index(output, `repo = "z/repo"`) > strings.Index(output, `path = "../local"`) {
		t.Fatalf("source declaration order changed:\n%s", output)
	}
	if strings.Index(output, `"alpha"`) > strings.Index(output, `"zeta"`) ||
		strings.Index(output, `"beta"`) > strings.Index(output, `"gamma"`) {
		t.Fatalf("skills not sorted:\n%s", output)
	}
	if strings.Contains(output, manifest.Path) || strings.Contains(output, "/runtime/") || strings.Contains(output, "ResolvedPath") {
		t.Fatalf("runtime paths encoded:\n%s", output)
	}

	path := filepath.Join(t.TempDir(), ".skillsrc")
	writeTestFile(t, path, output)
	reloaded, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest(encoded manifest): %v\n%s", err, output)
	}
	if got, want := reloaded.Sources[0].Repo, "z/repo"; got != want {
		t.Fatalf("first source repo = %q, want %q", got, want)
	}
	if got, want := reloaded.Sources[0].Skills, []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first source skills = %v, want %v", got, want)
	}
	if got, want := reloaded.Sources[1].Path, "../local"; got != want {
		t.Fatalf("second source path = %q, want %q", got, want)
	}
	if got, want := reloaded.Sources[1].Skills, []string{"beta", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second source skills = %v, want %v", got, want)
	}
}

func TestEncodeLockIsDeterministic(t *testing.T) {
	t.Parallel()
	lock := Lock{Version: 1, Sources: []LockSource{
		{Kind: SourceGit, Identity: "github.com/z/repo", Repo: "z/repo", Commit: strings.Repeat("b", 40), Skills: []LockedSkill{{Name: "z", Path: "skills/z", Hash: "sha256:z"}}},
		{Kind: SourceLocal, Identity: "local:./skills", Path: "./skills", Skills: []LockedSkill{{Name: "b", Path: "b", Hash: "sha256:b"}, {Name: "a", Path: "a", Hash: "sha256:a"}}},
	}}

	first, err := EncodeLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("lock output changed:\n%s\n---\n%s", first, second)
	}
	if strings.Index(string(first), `identity = "github.com/z/repo"`) > strings.Index(string(first), `identity = "local:./skills"`) {
		t.Fatalf("sources not sorted:\n%s", first)
	}
	if strings.Index(string(first), `name = "a"`) > strings.Index(string(first), `name = "b"`) {
		t.Fatalf("skills not sorted:\n%s", first)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
