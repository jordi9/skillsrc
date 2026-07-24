package skillsrc

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitOperationAcquiresEachRepositoryOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remoteOne, commitOne := makeGitRemote(t, map[string]string{"one/SKILL.md": "---\nname: one\n---\n"})
	remoteTwo, _ := makeGitRemote(t, map[string]string{"two/SKILL.md": "---\nname: two\n---\n"})
	op := NewGitOperation(filepath.Join(t.TempDir(), "cache"), "git")

	first, err := op.Resolve(ctx, remoteOne, "main", true, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := op.Resolve(ctx, remoteOne, "main", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if first != commitOne || second != commitOne || op.Acquisitions() != 1 {
		t.Fatalf("same repository: commits %q/%q, acquisitions %d", first, second, op.Acquisitions())
	}
	if _, err := op.Resolve(ctx, remoteTwo, "main", true, ""); err != nil {
		t.Fatal(err)
	}
	if op.Acquisitions() != 2 {
		t.Fatalf("two repositories caused %d acquisitions, want 2", op.Acquisitions())
	}
}

func TestGitOperationCachedCommitNeedsNoFetch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote, commit := makeGitRemote(t, map[string]string{"one/SKILL.md": "---\nname: one\n---\n"})
	cache := filepath.Join(t.TempDir(), "cache")
	first := NewGitOperation(cache, "git")
	if _, err := first.Resolve(ctx, remote, "main", true, ""); err != nil {
		t.Fatal(err)
	}

	second := NewGitOperation(cache, "git")
	if _, err := second.Resolve(ctx, remote, "main", false, commit); err != nil {
		t.Fatal(err)
	}
	if second.Acquisitions() != 0 {
		t.Fatalf("cached exact commit caused %d acquisitions", second.Acquisitions())
	}
}

func TestGitOperationFailedAcquisitionDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "eventual.git")
	cache := filepath.Join(root, "cache")
	first := NewGitOperation(cache, "git")
	if _, err := first.Resolve(ctx, remote, "main", true, ""); err == nil {
		t.Fatal("missing remote unexpectedly resolved")
	}

	_, commit := makeGitRemoteAt(t, remote, map[string]string{"one/SKILL.md": "---\nname: one\n---\n"})
	second := NewGitOperation(cache, "git")
	got, err := second.Resolve(ctx, remote, "main", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != commit || second.Acquisitions() != 1 {
		t.Fatalf("recovery commit %q acquisitions %d", got, second.Acquisitions())
	}
}

func TestGitMaterializationDoesNotFollowRepositorySymlinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote, _ := makeGitRemote(t, map[string]string{"skills/one/SKILL.md": "---\nname: one\n---\n"})
	pushGitSymlink(t, remote, "AGENTS.md", "outside")
	op := NewGitOperation(filepath.Join(t.TempDir(), "cache"), "git")
	commit, err := op.Resolve(ctx, remote, "main", true, "")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "tree")
	if err := op.Materialize(ctx, remote, commit, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(destination, "AGENTS.md"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("materialized symlink info=%v err=%v", info, err)
	}
	found, err := DiscoverSkills(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := found["one"]; !ok {
		t.Fatalf("selected skill not discoverable: %#v", found)
	}
}

func TestExtractTarCannotTraverseThroughArchivedSymlink(t *testing.T) {
	t.Parallel()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	if err := writer.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	body := []byte("escaped")
	if err := writer.WriteHeader(&tar.Header{Name: "link/pwned", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "tree")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(context.Background(), &data, destination); err == nil {
		t.Fatal("malicious archive unexpectedly extracted")
	}
	if _, err := os.Stat(filepath.Join(root, "outside", "pwned")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped destination: %v", err)
	}
}

func TestNormalizeRepository(t *testing.T) {
	t.Parallel()
	for input, identity := range map[string]string{
		"owner/repo":                          "github.com/owner/repo",
		"https://github.com/owner/repo.git":   "github.com/owner/repo",
		"git@github.com:owner/repo.git":       "github.com/owner/repo",
		"git@github.com/owner/repo":           "github.com/owner/repo",
		"ssh://git@github.com/owner/repo.git": "github.com/owner/repo",
	} {
		normalized, err := NormalizeRepository(input)
		if err != nil {
			t.Errorf("NormalizeRepository(%q): %v", input, err)
			continue
		}
		if normalized.Identity != identity {
			t.Errorf("NormalizeRepository(%q).Identity = %q", input, normalized.Identity)
		}
	}
}

func makeGitRemote(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	return makeGitRemoteAt(t, filepath.Join(t.TempDir(), "remote.git"), files)
}

func makeGitRemoteAt(t *testing.T, remote string, files map[string]string) (string, string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	runGitTest(t, "init", "-b", "main", work)
	runGitTest(t, "-C", work, "config", "user.email", "test@example.com")
	runGitTest(t, "-C", work, "config", "user.name", "Test")
	for name, content := range files {
		writeTestFile(t, filepath.Join(work, filepath.FromSlash(name)), content)
	}
	runGitTest(t, "-C", work, "add", ".")
	runGitTest(t, "-C", work, "commit", "-m", "fixture")
	commit := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "clone", "--bare", work, remote)
	return remote, commit
}

func pushGitSymlink(t *testing.T, remote, name, target string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	runGitTest(t, "clone", remote, work)
	runGitTest(t, "-C", work, "config", "user.email", "test@example.com")
	runGitTest(t, "-C", work, "config", "user.name", "Test")
	if err := os.Symlink(target, filepath.Join(work, name)); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", name)
	runGitTest(t, "-C", work, "commit", "-m", "symlink")
	runGitTest(t, "-C", work, "push", "origin", "main")
}

func runGitTest(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
