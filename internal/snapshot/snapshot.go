// Package snapshot creates immutable Git snapshots without modifying a
// developer's checkout, HEAD, or real index.
package snapshot

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Repo struct {
	WorkspaceRelativePath string  `json:"workspaceRelativePath"`
	SourcePath            string  `json:"sourcePath"`
	BaseSHA               *string `json:"baseSha"`
	SnapshotSHA           string  `json:"snapshotSha"`
	Bundle                string  `json:"bundle"`
	BundleSHA256          string  `json:"bundleSha256"`
}
type Manifest struct {
	Version       int             `json:"version"`
	ID            string          `json:"id"`
	WorkspacePath string          `json:"workspacePath"`
	CreatedAt     string          `json:"createdAt"`
	Provenance    json.RawMessage `json:"provenance,omitempty"`
	Repos         []Repo          `json:"repos"`
}

// SetProvenance records the submission context alongside an immutable bundle.
// It changes only manifest metadata; ReadVerified continues to verify every
// retained Git bundle digest before any reproduce/export operation.
func SetProvenance(manifestPath string, manifest *Manifest, provenance json.RawMessage) error {
	if manifest == nil || len(provenance) == 0 {
		return nil
	}
	manifest.Provenance = append(json.RawMessage(nil), provenance...)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(encoded, '\n'), 0o600)
}

func run(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
func id() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func digest(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// ReadVerified reads a retained manifest and verifies every bundle recorded
// alongside it.  Exporters use this before copying a snapshot so a portable
// package cannot silently preserve corrupt or substituted review state.
func ReadVerified(manifestPath string) (Manifest, error) {
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err = json.Unmarshal(contents, &manifest); err != nil || manifest.Version != 1 {
		return Manifest{}, errors.New("invalid snapshot manifest")
	}
	parent := filepath.Dir(manifestPath)
	for _, repo := range manifest.Repos {
		if filepath.Base(repo.Bundle) != repo.Bundle || repo.Bundle == "." || repo.Bundle == "" {
			return Manifest{}, fmt.Errorf("invalid snapshot bundle name: %q", repo.Bundle)
		}
		sum, err := digest(filepath.Join(parent, repo.Bundle))
		if err != nil || sum != repo.BundleSHA256 {
			return Manifest{}, fmt.Errorf("snapshot bundle hash mismatch: %s", repo.Bundle)
		}
	}
	return manifest, nil
}
func Capture(workspace, artifacts, base string) (Manifest, string, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return Manifest{}, "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(workspace); resolveErr == nil {
		workspace = resolved
	}
	repos, err := discover(workspace)
	if err != nil {
		return Manifest{}, "", err
	}
	snapshotID, err := id()
	if err != nil {
		return Manifest{}, "", err
	}
	dir := filepath.Join(artifacts, "snapshots", snapshotID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Manifest{}, "", err
	}
	captured := make([]Repo, 0, len(repos))
	for _, root := range repos {
		rel, _ := filepath.Rel(workspace, root)
		if rel == "." {
			rel = "."
		}
		slug := strings.NewReplacer("/", "_", "\\", "_").Replace(rel)
		if slug == "." {
			slug = "root"
		}
		index := filepath.Join(dir, "index-"+slug)
		env := []string{"GIT_INDEX_FILE=" + index}
		baseSHA, e := run(root, nil, "rev-parse", first(base, "HEAD"))
		var basePtr *string
		if e == nil {
			basePtr = &baseSHA
			_, e = run(root, env, "read-tree", baseSHA)
		} else {
			_, e = run(root, env, "read-tree", "--empty")
		}
		if e != nil {
			return Manifest{}, "", e
		}
		// An independent Git repository nested below this repository is part of
		// the workspace too, but Git refuses to add an unborn nested repository
		// to the parent's temporary index (and should not turn it into a gitlink
		// in any case). Capture nested repositories as their own immutable
		// bundles and exclude their directory from the parent's projection.
		addArgs := []string{"add", "-A", "--", "."}
		for _, candidate := range repos {
			nested, relErr := filepath.Rel(root, candidate)
			if relErr != nil || nested == "." || nested == ".." || strings.HasPrefix(nested, ".."+string(filepath.Separator)) {
				continue
			}
			addArgs = append(addArgs, ":(exclude)"+filepath.ToSlash(nested))
		}
		if _, e = run(root, env, addArgs...); e != nil {
			return Manifest{}, "", e
		}
		tree, e := run(root, env, "write-tree")
		if e != nil {
			return Manifest{}, "", e
		}
		args := []string{"commit-tree", tree, "-m", "cmux-localreview snapshot " + snapshotID}
		if basePtr != nil {
			args = append(args, "-p", *basePtr)
		}
		sha, e := run(root, env, args...)
		os.Remove(index)
		if e != nil {
			return Manifest{}, "", e
		}
		ref := "refs/cmux-localreview/snapshots/" + snapshotID + "/" + slug
		if _, e = run(root, nil, "update-ref", ref, sha); e != nil {
			return Manifest{}, "", e
		}
		name := slug + ".bundle"
		bundle := filepath.Join(dir, name)
		if _, e = run(root, nil, "bundle", "create", bundle, ref); e != nil {
			return Manifest{}, "", e
		}
		sum, e := digest(bundle)
		if e != nil {
			return Manifest{}, "", e
		}
		captured = append(captured, Repo{WorkspaceRelativePath: rel, SourcePath: root, BaseSHA: basePtr, SnapshotSHA: sha, Bundle: name, BundleSHA256: sum})
	}
	m := Manifest{Version: 1, ID: snapshotID, WorkspacePath: workspace, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Repos: captured}
	path := filepath.Join(dir, "manifest.json")
	encoded, _ := json.MarshalIndent(m, "", "  ")
	if err = os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
		return Manifest{}, "", err
	}
	return m, path, nil
}
func discover(workspace string) ([]string, error) {
	found := []string{}
	seen := map[string]bool{}
	err := filepath.WalkDir(workspace, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		root, err := run(path, nil, "rev-parse", "--show-toplevel")
		if err == nil && !seen[root] {
			seen[root] = true
			found = append(found, root)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, errors.New("no Git repositories found below workspace")
	}
	// filepath.WalkDir is normally lexical, but sorting here makes manifests
	// stable across supported filesystems and keeps the parent-before-child
	// snapshot order obvious to callers.
	sort.Strings(found)
	return found, nil
}
func first(v, f string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return f
}

// Materialize verifies retained bundle digests and reconstructs every
// workspace-relative repository into an empty review destination.
func Materialize(manifestPath, destination string) (Manifest, error) {
	manifest, err := ReadVerified(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	if err = os.MkdirAll(destination, 0700); err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(manifestPath)
	for _, repo := range manifest.Repos {
		bundle := filepath.Join(parent, repo.Bundle)
		target := destination
		if repo.WorkspaceRelativePath != "." {
			target = filepath.Join(destination, repo.WorkspaceRelativePath)
		}
		if err = os.MkdirAll(target, 0700); err != nil {
			return Manifest{}, err
		}
		if _, err = run(target, nil, "init"); err != nil {
			return Manifest{}, err
		}
		if _, err = run(target, nil, "fetch", bundle, repo.SnapshotSHA); err != nil {
			return Manifest{}, err
		}
		if _, err = run(target, nil, "checkout", "-B", "localreview/review-"+manifest.ID[:8], repo.SnapshotSHA); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}
