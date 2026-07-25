package skillsrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetDisableModelInvocationPreservesReadOnlyMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	writeTestFile(t, path, "---\nname: one\n---\nbody\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}

	if err := setDisableModelInvocation(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "disable-model-invocation: true") {
		t.Fatalf("override missing:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("mode = %o, want 444", got)
	}
}

func TestSetDisableModelInvocationInsertsRootFrontmatterKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	writeTestFile(t, path, "---\nname: one\nmetadata:\n  disable-model-invocation: false\n---\nbody\n")

	if err := setDisableModelInvocation(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "  disable-model-invocation: false\ndisable-model-invocation: true\n---") {
		t.Fatalf("root key was not inserted before the closing delimiter:\n%s", content)
	}
}
