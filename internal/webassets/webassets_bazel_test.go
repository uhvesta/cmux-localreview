//go:build bazel_ui_archive

package webassets

import (
	"io/fs"
	"strings"
	"testing"
)

// This is intentionally a source-to-binary freshness assertion, not merely a
// generic index-page test. It caught the previous release bug where a daemon
// built by Bazel embedded an older checked-in renderer than the current React
// source. The marker is ordinary Queue Home copy from the native federation
// UI, so changing it requires deliberately updating this assertion.
func TestBazelEmbeddedAssetsContainCurrentQueueHome(t *testing.T) {
	assets, err := FS()
	if err != nil {
		t.Fatalf("load Bazel embedded UI: %v", err)
	}
	const marker = "Connect loopback-only remote daemons through an on-demand SSH forward."
	found := false
	err = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return walkErr
		}
		contents, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), marker) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk Bazel embedded UI: %v", err)
	}
	if !found {
		t.Fatalf("Bazel embedded UI does not contain current Queue Home federation copy")
	}
}
