// Package setup installs the small, managed Copilot skill surface used by the
// native localreview CLI. It never replaces user-owned instruction files.
package setup

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	managed = "cmux-localreview-managed"
	start   = "<!-- cmux-localreview:start -->"
	end     = "<!-- cmux-localreview:end -->"
)

//go:embed all:templates
var templates embed.FS

type Options struct {
	Workspace string
	Personal  bool
	Project   bool
	DryRun    bool
	Command   string
}

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

func command(value string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "localreview queue-submit"
}

func read(name string) (string, error) {
	b, err := templates.ReadFile("templates/" + name)
	return string(b), err
}

func markedSkill(contents string) string {
	if strings.Contains(contents, managed) {
		return contents
	}
	if strings.HasPrefix(contents, "---\n") {
		if close := strings.Index(contents[4:], "\n---\n"); close >= 0 {
			at := 4 + close + len("\n---\n")
			return contents[:at] + "\n<!-- " + managed + ": skill -->\n" + contents[at:]
		}
	}
	return "<!-- " + managed + ": skill -->\n" + contents
}

func writeManaged(path, contents string, dryRun bool, changes *[]Change) error {
	current, err := os.ReadFile(path)
	if err == nil {
		if string(current) == contents {
			*changes = append(*changes, Change{Path: path, Action: "unchanged"})
			return nil
		}
		if !strings.Contains(string(current), managed) {
			*changes = append(*changes, Change{Path: path, Action: "skipped", Reason: "existing file is not managed by cmux-localreview"})
			return nil
		}
		*changes = append(*changes, Change{Path: path, Action: "updated"})
	} else if !os.IsNotExist(err) {
		return err
	} else {
		*changes = append(*changes, Change{Path: path, Action: "created"})
	}
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o644)
}

func mergeInstructions(path, block string, dryRun bool, changes *[]Change) error {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeManaged(path, block, dryRun, changes)
	}
	if err != nil {
		return err
	}
	current := string(contents)
	from, to := strings.Index(current, start), strings.Index(current, end)
	next := ""
	if from >= 0 && to >= from {
		next = current[:from] + block + strings.TrimPrefix(current[to+len(end):], "\n")
	} else {
		next = strings.TrimSpace(current) + "\n\n" + block
	}
	if next == current {
		*changes = append(*changes, Change{Path: path, Action: "unchanged"})
		return nil
	}
	*changes = append(*changes, Change{Path: path, Action: "updated", Reason: map[bool]string{true: "", false: "appended managed section"}[from >= 0]})
	if dryRun {
		return nil
	}
	return os.WriteFile(path, []byte(next), 0o644)
}

func installSkills(dir string, cmd string, dryRun bool, changes *[]Change) error {
	entries, err := fs.ReadDir(templates, "templates/skills")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		contents, err := read("skills/" + entry.Name() + "/SKILL.md")
		if err != nil {
			return err
		}
		contents = strings.ReplaceAll(markedSkill(contents), "localreview queue-submit", cmd)
		if err := writeManaged(filepath.Join(dir, entry.Name(), "SKILL.md"), contents, dryRun, changes); err != nil {
			return err
		}
	}
	return nil
}

// Install configures project and/or personal Copilot skill locations.
func Install(options Options) ([]Change, error) {
	if !options.Project && !options.Personal {
		return nil, fmt.Errorf("choose project setup or personal setup")
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return nil, err
	}
	cmd := command(options.Command)
	changes := []Change{}
	if options.Project {
		if err := installSkills(filepath.Join(workspace, ".github", "skills"), cmd, options.DryRun, &changes); err != nil {
			return nil, err
		}
		instructions, err := read("copilot-instructions.md")
		if err != nil {
			return nil, err
		}
		block := start + "\n<!-- " + managed + ": project instructions -->\n" + strings.TrimSpace(instructions) + "\n\n## Installed command\n\n```sh\n" + cmd + " . --title \"<review title>\"\n```\n" + end + "\n"
		if err := mergeInstructions(filepath.Join(workspace, ".github", "copilot-instructions.md"), block, options.DryRun, &changes); err != nil {
			return nil, err
		}
	}
	if options.Personal {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		if err := installSkills(filepath.Join(home, ".copilot", "skills"), cmd, options.DryRun, &changes); err != nil {
			return nil, err
		}
	}
	return changes, nil
}
