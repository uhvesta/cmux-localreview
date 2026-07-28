// localreview is the Go control-plane CLI. It intentionally talks to the
// loopback daemon capability instead of opening SQLite itself, so every CLI
// mutation follows the same authentication and audit boundary as Queue Home.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/daemon"
	copilotsetup "github.com/uhvesta/cmux-localreview/internal/setup"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

type discovery struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func dataDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CMUX_LOCALREVIEW_DATA_DIR")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "cmux-localreview"), nil
}
func runDaemon(args []string) error {
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	port := flags.Int("port", 0, "loopback port")
	data := flags.String("data-dir", "", "data directory")
	ui := flags.String("ui-dir", "", "built web application directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := daemon.Start(ctx, daemon.Options{Port: *port, DataDir: *data, UIDir: *ui})
	if err != nil {
		return err
	}
	fmt.Printf("cmux-localreview Go daemon listening on 127.0.0.1:%d\n", d.Port())
	<-ctx.Done()
	return d.Close()
}
func discovered() (discovery, error) {
	dir, err := dataDir()
	if err != nil {
		return discovery{}, err
	}
	contents, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		return discovery{}, errors.New("localreviewd is not running; start `localreview daemon`")
	}
	var value discovery
	if err := json.Unmarshal(contents, &value); err != nil || value.Port == 0 || value.Token == "" {
		return discovery{}, errors.New("invalid localreviewd discovery record")
	}
	return value, nil
}

// daemonCall is the only HTTP boundary used by native CLI subcommands. The
// daemon capability is read from its owner-only discovery record and never
// printed or stored by the CLI.
func daemonCall(method, apiPath string, body any) (int, json.RawMessage, error) {
	d, err := discovered()
	if err != nil {
		return 0, nil, err
	}
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d/api%s", d.Port, apiPath), input)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(contents))
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(contents, &payload)
		if payload.Error != "" {
			message = payload.Error
		}
		return response.StatusCode, nil, fmt.Errorf("daemon %s %s failed (%s): %s", method, apiPath, response.Status, message)
	}
	return response.StatusCode, json.RawMessage(contents), nil
}

func printDaemonJSON(method, path string, body any) error {
	_, response, err := daemonCall(method, path, body)
	if err != nil {
		return err
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, response, "", "  ") == nil {
		fmt.Println(formatted.String())
	} else {
		fmt.Println(string(response))
	}
	return nil
}

func authCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: localreview auth <login|status|logout> [options]")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview auth status")
		}
		return printDaemonJSON(http.MethodGet, "/github/auth/status", nil)
	case "logout":
		if len(args) != 2 {
			return errors.New("usage: localreview auth logout <read|write|copilot>")
		}
		return printDaemonJSON(http.MethodDelete, "/github/auth/"+args[1], nil)
	case "login":
		flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
		capability := flags.String("capability", "copilot", "GitHub App capability: read, write, or copilot")
		clientID := flags.String("client-id", "", "public GitHub App client ID to configure before login")
		clientSecretStdin := flags.Bool("client-secret-stdin", false, "read the OAuth client secret from stdin and store it in the OS secret store")
		device := flags.Bool("device", false, "use device flow instead of the default loopback browser OAuth flow")
		noWait := flags.Bool("no-wait", false, "print authorization instructions without polling")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: localreview auth login [--capability CAPABILITY] [--client-id ID] [--client-secret-stdin] [--device] [--no-wait]")
		}
		if *capability != "read" && *capability != "write" && *capability != "copilot" {
			return errors.New("capability must be read, write, or copilot")
		}
		if *clientSecretStdin && *clientID == "" {
			return errors.New("--client-secret-stdin requires --client-id")
		}
		if *clientID != "" {
			configure := map[string]string{"capability": *capability, "clientId": *clientID}
			if *clientSecretStdin {
				secret, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
				if err != nil {
					return fmt.Errorf("read OAuth client secret from stdin: %w", err)
				}
				configure["clientSecret"] = strings.TrimSpace(string(secret))
			}
			if _, _, err := daemonCall(http.MethodPost, "/github/auth/configure", configure); err != nil {
				return err
			}
		}
		flow := "loopback"
		if *device {
			flow = "device"
		}
		_, response, err := daemonCall(http.MethodPost, "/github/auth/"+*capability+"/start", map[string]string{"flow": flow})
		if err != nil {
			return err
		}
		var start struct {
			Flow             string `json:"flow"`
			AuthorizationURL string `json:"authorizationUrl"`
			UserCode         string `json:"userCode"`
			VerificationURI  string `json:"verificationUri"`
		}
		if err := json.Unmarshal(response, &start); err != nil {
			return err
		}
		if start.Flow == "loopback" {
			fmt.Printf("Open %s to authorize GitHub %s.\n", start.AuthorizationURL, *capability)
		} else {
			fmt.Printf("Open %s and enter code %s.\n", start.VerificationURI, start.UserCode)
		}
		if *noWait {
			return nil
		}
		for {
			time.Sleep(2 * time.Second)
			_, result, err := daemonCall(http.MethodPost, "/github/auth/"+*capability+"/poll", nil)
			if err != nil {
				return err
			}
			var status struct {
				Authenticated bool   `json:"authenticated"`
				State         string `json:"loginState"`
				Error         string `json:"error"`
			}
			if err := json.Unmarshal(result, &status); err != nil {
				return err
			}
			if status.Authenticated {
				fmt.Printf("GitHub %s capability connected.\n", *capability)
				return nil
			}
			if status.State == "error" || status.Error != "" {
				return fmt.Errorf("GitHub %s login failed: %s", *capability, status.Error)
			}
		}
	default:
		return errors.New("usage: localreview auth <login|status|logout> [options]")
	}
}

func remoteCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: localreview remote <submit|status> [options]")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: localreview remote status")
		}
		return printDaemonJSON(http.MethodGet, "/federation/nodes", nil)
	case "submit":
		flags := flag.NewFlagSet("remote submit", flag.ContinueOnError)
		workspace := flags.String("workspace", "", "local cache/worktree path recorded for this PR")
		title := flags.String("title", "", "review title")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: localreview remote submit [--workspace PATH] [--title TITLE] <PR URL>")
		}
		path := *workspace
		if path == "" {
			path, _ = os.Getwd()
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if *title == "" {
			*title = "Review " + flags.Arg(0)
		}
		return printDaemonJSON(http.MethodPost, "/queue", map[string]string{"title": *title, "workspacePath": absolute, "kind": "remote", "remoteUrl": flags.Arg(0)})
	default:
		return errors.New("usage: localreview remote <submit|status> [options]")
	}
}
func submit(args []string) error {
	flags := flag.NewFlagSet("queue-submit", flag.ContinueOnError)
	title := flags.String("title", "", "queue title")
	topic := flags.String("topic", "", "stable review topic")
	kind := flags.String("kind", "local", "local or remote")
	remoteURL := flags.String("remote-url", "", "remote pull request URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: localreview queue-submit [--title TITLE] [--topic TOPIC] <workspace-path>")
	}
	workspace, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return err
	}
	if *title == "" {
		*title = "Review " + workspace
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	_, manifestPath, err := snapshot.Capture(workspace, filepath.Join(dir, "artifacts"), "")
	if err != nil {
		return err
	}
	d, err := discovered()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"title": *title, "workspacePath": workspace, "reviewTopic": *topic, "kind": *kind, "remoteUrl": *remoteURL, "snapshotManifestPath": manifestPath})
	request, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/queue", d.Port), bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+d.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return fmt.Errorf("queue submission failed: %s", response.Status)
	}
	var result map[string]any
	_ = json.NewDecoder(response.Body).Decode(&result)
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	return nil
}
func reproduce(args []string) error {
	flags := flag.NewFlagSet("reproduce", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: localreview reproduce <manifest.json> <empty-destination>")
	}
	destination, err := filepath.Abs(flags.Arg(1))
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(destination); err == nil && len(entries) > 0 {
		return errors.New("destination must be empty")
	}
	manifest, err := snapshot.Materialize(flags.Arg(0), destination)
	if err != nil {
		return err
	}
	fmt.Printf("Reproduced snapshot %s\ncwd: %s\n", manifest.ID, destination)
	return nil
}

// setupCopilot installs only cmux-localreview-managed Copilot instructions and
// skills. Existing user-owned files are preserved, and --dry-run has no write
// side effects so it is safe for remote/bootstrap automation.
func setupCopilot(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	personal := flags.Bool("personal", false, "also install skills in ~/.copilot/skills")
	noProject := flags.Bool("no-project", false, "do not install project-local .github skills")
	dryRun := flags.Bool("dry-run", false, "show planned changes without writing")
	jsonOutput := flags.Bool("json", false, "print machine-readable changes")
	command := flags.String("command", "", "submission command written into installed skills")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: localreview setup [--personal] [--no-project] [--dry-run] [workspace]")
	}
	workspace := "."
	if flags.NArg() == 1 {
		workspace = flags.Arg(0)
	}
	changes, err := copilotsetup.Install(copilotsetup.Options{Workspace: workspace, Project: !*noProject, Personal: *personal, DryRun: *dryRun, Command: *command})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"workspace": workspace, "dryRun": *dryRun, "changes": changes})
	}
	for _, change := range changes {
		if change.Reason == "" {
			fmt.Printf("%-9s %s\n", change.Action, change.Path)
		} else {
			fmt.Printf("%-9s %s — %s\n", change.Action, change.Path, change.Reason)
		}
	}
	return nil
}

// openHome creates the one-time browser capability URL. The daemon token is
// deliberately kept in the URL fragment: browsers never transmit fragments
// to the HTTP server, and the React client exchanges it immediately for an
// HttpOnly, same-origin cookie.
func openHome(args []string) error {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	noOpen := flags.Bool("no-open", false, "print the Queue Home or workspace-review URL without launching a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: localreview open [--no-open] [workspace-path]")
	}
	d, err := discovered()
	if err != nil {
		return err
	}
	path := "/"
	if flags.NArg() == 1 {
		workspace, err := filepath.Abs(flags.Arg(0))
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"workspacePath": workspace})
		request, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/workspaces/open", d.Port), bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+d.Token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("workspace activation failed: %s", response.Status)
		}
		path = "/review"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s#daemonToken=%s", d.Port, path, d.Token)
	fmt.Println(url)
	if *noOpen {
		return nil
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("Queue Home URL printed above, but could not launch a browser: %w", err)
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: localreview <daemon|open|queue-submit|reproduce|setup|auth|remote> [options]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon":
		if err := runDaemon(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "queue-submit":
		if err := submit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "reproduce":
		if err := reproduce(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "setup":
		if err := setupCopilot(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "auth":
		if err := authCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "remote":
		if err := remoteCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	case "open":
		if err := openHome(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "localreview:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: localreview <daemon|open|queue-submit|reproduce|setup|auth|remote> [options]")
		os.Exit(2)
	}
}
