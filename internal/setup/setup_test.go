package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectInstallIsManagedAndIdempotent(t *testing.T) {
	workspace := t.TempDir()
	changes, err := Install(Options{Workspace: workspace, Project: true})
	if err != nil || len(changes) != 4 {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	path := filepath.Join(workspace, ".github", "skills", "localreview-submit", "SKILL.md")
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), managed) || !strings.Contains(string(contents), "localreview queue-submit") {
		t.Fatalf("skill not installed correctly: %v %q", err, contents)
	}
	changes, err = Install(Options{Workspace: workspace, Project: true})
	if err != nil || changes[0].Action != "unchanged" {
		t.Fatalf("idempotence changes=%#v err=%v", changes, err)
	}
}

func TestInstallPreservesUnmanagedSkillAndCanDryRun(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".github", "skills", "localreview-submit", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := Install(Options{Workspace: workspace, Project: true, DryRun: true})
	skipped := false
	for _, change := range changes {
		if change.Path == path && change.Action == "skipped" {
			skipped = true
		}
	}
	if err != nil || !skipped {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != "custom" {
		t.Fatalf("unmanaged skill changed: %q", contents)
	}
}
