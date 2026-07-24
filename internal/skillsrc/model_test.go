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
