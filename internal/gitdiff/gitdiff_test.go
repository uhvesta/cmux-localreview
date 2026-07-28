package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func repo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	runGit(t, d, "init")
	runGit(t, d, "config", "user.email", "test@example.com")
	runGit(t, d, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, d, "add", ".")
	runGit(t, d, "commit", "-m", "initial")
	return d
}
func TestParseCommittedDiffAndLineNumbers(t *testing.T) {
	d := repo(t)
	if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte("one\nchanged\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, d, "commit", "-am", "change")
	r, err := Parse(d, Selection{BaseCommitish: "HEAD^", TargetCommitish: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsEmpty || len(r.Files) != 1 {
		t.Fatalf("files=%+v", r.Files)
	}
	f := r.Files[0]
	if f.Path != "file.txt" || f.Status != "modified" || f.Additions != 2 || f.Deletions != 1 {
		t.Fatalf("file=%+v", f)
	}
	if len(f.Chunks) != 1 || f.Chunks[0].OldStart != 1 || f.Chunks[0].NewStart != 1 {
		t.Fatalf("chunks=%+v", f.Chunks)
	}
	var add, del Line
	for _, l := range f.Chunks[0].Lines {
		if l.Type == "add" && l.Content == "changed" {
			add = l
		}
		if l.Type == "delete" {
			del = l
		}
	}
	if add.NewLineNumber == nil || *add.NewLineNumber != 2 || add.OldLineNumber != nil {
		t.Fatalf("add=%+v", add)
	}
	if del.OldLineNumber == nil || *del.OldLineNumber != 2 || del.NewLineNumber != nil {
		t.Fatalf("del=%+v", del)
	}
}
func TestParseDefaultPrefersDirtyWorkspace(t *testing.T) {
	d := repo(t)
	if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte("dirty\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Parse(d, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if r.TargetCommitish != "." || len(r.Files) != 1 || r.Files[0].Additions != 1 {
		t.Fatalf("response=%+v", r)
	}
}
func TestParseAddedDeletedAndRenamed(t *testing.T) {
	d := repo(t)
	if err := os.WriteFile(filepath.Join(d, "new.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, d, "rm", "file.txt")
	runGit(t, d, "add", "new.txt")
	runGit(t, d, "commit", "-m", "replace")
	r, err := Parse(d, Selection{BaseCommitish: "HEAD^", TargetCommitish: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 2 {
		t.Fatalf("files=%+v", r.Files)
	}
	seen := map[string]string{}
	for _, f := range r.Files {
		seen[f.Path] = f.Status
	}
	if seen["new.txt"] != "added" || seen["file.txt"] != "deleted" {
		t.Fatalf("seen=%v", seen)
	}
	raw := "diff --git a/old.txt b/new.txt\nsimilarity index 100%\nrename from old.txt\nrename to new.txt\n"
	f := ParseUnified(raw)
	if len(f) != 1 || f[0].Status != "renamed" || f[0].OldPath == nil || *f[0].OldPath != "old.txt" {
		t.Fatalf("rename=%+v", f)
	}
}
