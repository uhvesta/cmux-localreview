package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFederationSSHConfigArgsAreExplicitAndSafe(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(config, []byte("Host lab\n  HostName example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMUX_LOCALREVIEW_SSH_CONFIG", config)
	args, err := federationSSHConfigArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "-F" || args[1] != config {
		t.Fatalf("config args=%q", args)
	}
	if err := os.Chmod(config, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := federationSSHConfigArgs(); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("insecure config error=%v", err)
	}
	t.Setenv("CMUX_LOCALREVIEW_SSH_CONFIG", filepath.Join(dir, "missing"))
	if _, err := federationSSHConfigArgs(); err == nil || !strings.Contains(err.Error(), "read CMUX_LOCALREVIEW_SSH_CONFIG") {
		t.Fatalf("missing config error=%v", err)
	}
}
