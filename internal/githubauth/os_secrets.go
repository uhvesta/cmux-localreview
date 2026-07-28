package githubauth

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// CommandRunner is deliberately injectable so the credential boundary can be
// tested without interacting with a developer's keychain. Implementations must
// not log stdin, stdout, or the commands' credential arguments.
type CommandRunner interface {
	Run(name string, args []string, stdin []byte) (stdout []byte, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) Run(name string, args []string, stdin []byte) ([]byte, []byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// OSSecretStore stores GitHub capability credentials in the platform's
// credential service. There is intentionally no plaintext-file fallback:
// unsupported systems fail closed and cannot authenticate.
type OSSecretStore struct {
	OS     string
	Runner CommandRunner
}

func NewOSSecretStore() (*OSSecretStore, error) {
	s := &OSSecretStore{OS: runtime.GOOS, Runner: execRunner{}}
	if s.OS != "darwin" && s.OS != "linux" {
		return nil, fmt.Errorf("no supported system secret store for %s", s.OS)
	}
	return s, nil
}
func (s *OSSecretStore) runner() CommandRunner {
	if s.Runner == nil {
		return execRunner{}
	}
	return s.Runner
}
func (s *OSSecretStore) Get(service, account string) (string, error) {
	var out, errOut []byte
	var err error
	switch s.OS {
	case "darwin":
		out, errOut, err = s.runner().Run("security", []string{"find-generic-password", "-s", service, "-a", account, "-w"}, nil)
	case "linux":
		out, errOut, err = s.runner().Run("secret-tool", []string{"lookup", "service", service, "account", account}, nil)
	default:
		return "", fmt.Errorf("no supported system secret store for %s", s.OS)
	}
	if err != nil {
		// Both tools signal an absent entry with nonzero exit. Treat it as no
		// credential; callers never need the platform-specific diagnostic.
		if absent(errOut) {
			return "", nil
		}
		return "", fmt.Errorf("system secret lookup failed: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
func (s *OSSecretStore) Set(service, account, value string) error {
	if value == "" {
		return errors.New("refusing to store an empty credential")
	}
	var errOut []byte
	var err error
	switch s.OS {
	case "darwin":
		_, errOut, err = s.runner().Run("security", []string{"add-generic-password", "-U", "-s", service, "-a", account, "-w", value}, nil)
	case "linux":
		_, errOut, err = s.runner().Run("secret-tool", []string{"store", "--label=" + service, "service", service, "account", account}, []byte(value))
	default:
		return fmt.Errorf("no supported system secret store for %s", s.OS)
	}
	if err != nil {
		return fmt.Errorf("system secret write failed: %w", err)
	}
	_ = errOut // never expose tool output; it can contain sensitive metadata.
	return nil
}
func (s *OSSecretStore) Delete(service, account string) error {
	var errOut []byte
	var err error
	switch s.OS {
	case "darwin":
		_, errOut, err = s.runner().Run("security", []string{"delete-generic-password", "-s", service, "-a", account}, nil)
	case "linux":
		_, errOut, err = s.runner().Run("secret-tool", []string{"clear", "service", service, "account", account}, nil)
	default:
		return fmt.Errorf("no supported system secret store for %s", s.OS)
	}
	if err != nil && !absent(errOut) {
		return fmt.Errorf("system secret delete failed: %w", err)
	}
	return nil
}
func absent(stderr []byte) bool {
	v := strings.ToLower(string(stderr))
	return strings.Contains(v, "could not be found") || strings.Contains(v, "not found") || strings.Contains(v, "no such") || strings.Contains(v, "no matching")
}
