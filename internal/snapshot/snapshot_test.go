package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}
func TestCaptureKeepsWorkingTreeAndWritesVerifiableBundle(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, path, err := Capture(root, filepath.Join(t.TempDir(), "artifacts"), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Repos[0].SnapshotSHA == "" || path == "" {
		t.Fatal("missing snapshot")
	}
	current, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(current) != "dirty\n" {
		t.Fatalf("working tree changed: %q %v", current, err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(path), m.Repos[0].Bundle)); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureIncludesSiblingRepositories(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"api", "web"} {
		root := filepath.Join(workspace, name)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, root, "init")
		git(t, root, "config", "user.email", "test@example.com")
		git(t, root, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, root, "add", ".")
		git(t, root, "commit", "-m", "initial")
	}
	m, manifest, err := Capture(workspace, filepath.Join(t.TempDir(), "artifacts"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repos) != 2 {
		t.Fatalf("repos=%#v", m.Repos)
	}
	seen := map[string]bool{}
	for _, repo := range m.Repos {
		seen[repo.WorkspaceRelativePath] = true
		if _, err := os.Stat(filepath.Join(filepath.Dir(manifest), repo.Bundle)); err != nil {
			t.Fatal(err)
		}
	}
	if !seen["api"] || !seen["web"] {
		t.Fatalf("paths=%v", seen)
	}
}

func TestCaptureIncludesNestedRepositoriesWithoutAddingGitlinks(t *testing.T) {
	workspace := t.TempDir()
	git(t, workspace, "init")
	git(t, workspace, "config", "user.email", "test@example.com")
	git(t, workspace, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workspace, "parent.txt"), []byte("parent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, workspace, "add", "parent.txt")
	git(t, workspace, "commit", "-m", "parent")

	nested := filepath.Join(workspace, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init")
	git(t, nested, "config", "user.email", "test@example.com")
	git(t, nested, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(nested, "child.txt"), []byte("child\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "add", "child.txt")
	git(t, nested, "commit", "-m", "child")
	if err := os.WriteFile(filepath.Join(nested, "untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}

	manifest, manifestPath, err := Capture(workspace, filepath.Join(t.TempDir(), "artifacts"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 2 {
		t.Fatalf("repos=%#v", manifest.Repos)
	}
	seen := map[string]bool{}
	for _, repo := range manifest.Repos {
		seen[repo.WorkspaceRelativePath] = true
	}
	if !seen["."] || !seen["nested"] {
		t.Fatalf("paths=%v", seen)
	}
	destination := filepath.Join(t.TempDir(), "reproduced")
	if _, err := Materialize(manifestPath, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "nested", "untracked.txt")); err != nil {
		t.Fatalf("nested working tree file missing after materialization: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "nested", ".git")); err != nil {
		t.Fatalf("nested repository was not materialized independently: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git", "modules", "nested")); !os.IsNotExist(err) {
		t.Fatalf("parent snapshot unexpectedly captured nested gitlink: %v", err)
	}
}

func TestMaterializeRestoresSiblingRepositories(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"api", "web"} {
		root := filepath.Join(workspace, name)
		os.MkdirAll(root, 0700)
		git(t, root, "init")
		git(t, root, "config", "user.email", "test@example.com")
		git(t, root, "config", "user.name", "Test")
		os.WriteFile(filepath.Join(root, "main.txt"), []byte(name+"\n"), 0600)
		git(t, root, "add", ".")
		git(t, root, "commit", "-m", "initial")
	}
	_, manifest, err := Capture(workspace, filepath.Join(t.TempDir(), "artifacts"), "")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "reproduced")
	if _, err = Materialize(manifest, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "web"} {
		contents, err := os.ReadFile(filepath.Join(destination, name, "main.txt"))
		if err != nil || string(contents) != name+"\n" {
			t.Fatalf("%s: %q %v", name, contents, err)
		}
	}
}
