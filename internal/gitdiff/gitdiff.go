// Package gitdiff reads Git's unified diff format into the small JSON shape
// consumed by the retained difit web client.  It deliberately shells out to
// Git: Git is the authority for revision syntax, attributes, and the index.
package gitdiff

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type File struct {
	Path      string  `json:"path"`
	OldPath   *string `json:"oldPath,omitempty"`
	Status    string  `json:"status"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Chunks    []Chunk `json:"chunks"`
	// IsGenerated is computed by the daemon from the selected file content.
	// Keep the field even when false: the reviewer uses it to decide whether a
	// diff should be collapsed by default.
	IsGenerated bool `json:"isGenerated"`
}

type Chunk struct {
	Header   string `json:"header"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Lines    []Line `json:"lines"`
}

type Line struct {
	Type          string `json:"type"`
	Content       string `json:"content"`
	OldLineNumber *int   `json:"oldLineNumber,omitempty"`
	NewLineNumber *int   `json:"newLineNumber,omitempty"`
}

// Selection accepts the same special targets as difit: working (unstaged),
// staged, and . (all changes relative to BaseCommitish).  Empty values select
// all dirty changes against HEAD, otherwise HEAD^..HEAD.
type Selection struct {
	BaseCommitish    string
	TargetCommitish  string
	IgnoreWhitespace bool
	ContextLines     *int
}

type Response struct {
	Commit string `json:"commit"`
	Files  []File `json:"files"`
	// These false values are meaningful to the reviewer (and were present in
	// the frozen TS contract), so do not hide them behind omitempty.
	IgnoreWhitespace         bool   `json:"ignoreWhitespace"`
	IsEmpty                  bool   `json:"isEmpty"`
	BaseCommitish            string `json:"baseCommitish,omitempty"`
	TargetCommitish          string `json:"targetCommitish,omitempty"`
	RequestedBaseCommitish   string `json:"requestedBaseCommitish,omitempty"`
	RequestedTargetCommitish string `json:"requestedTargetCommitish,omitempty"`
	RequestedBaseMode        string `json:"requestedBaseMode,omitempty"`
	RepositoryID             string `json:"repositoryId,omitempty"`
	OpenInEditorAvailable    bool   `json:"openInEditorAvailable"`
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func rev(repo, ref string) (string, error) { return git(repo, "rev-parse", ref) }
func short(ref string) string {
	if len(ref) > 7 {
		return ref[:7]
	}
	return ref
}

// Parse invokes Git once for the selected diff and parses that output.
func Parse(repo string, selection Selection) (Response, error) {
	base, target := selection.BaseCommitish, selection.TargetCommitish
	rootCommit := false
	if base == "" && target == "" {
		dirty, err := git(repo, "status", "--porcelain")
		if err != nil {
			return Response{}, err
		}
		if dirty != "" {
			base, target = "HEAD", "."
		} else {
			if _, err := rev(repo, "HEAD^"); err == nil {
				base, target = "HEAD^", "HEAD"
			} else {
				// A repository with its first commit has no parent, but it is
				// still a perfectly reviewable revision.
				base, target, rootCommit = "", "HEAD", true
			}
		}
	}
	if base == "" && !rootCommit {
		base = "HEAD"
	}
	if target == "" {
		target = "."
	}

	args := []string{"diff", "--no-ext-diff", "--color=never"}
	if selection.IgnoreWhitespace {
		args = append(args, "-w")
	}
	if selection.ContextLines != nil {
		args = append(args, "-U"+strconv.Itoa(*selection.ContextLines))
	}

	result := Response{IgnoreWhitespace: selection.IgnoreWhitespace, RequestedBaseCommitish: base, RequestedTargetCommitish: target, OpenInEditorAvailable: true}
	if rootCommit {
		targetHash, err := rev(repo, target)
		if err != nil {
			return Response{}, err
		}
		// `git diff --root` is not a portable revision comparison. Compare the
		// initial tree with Git's canonical empty-tree object instead.
		args = append(args, "4b825dc642cb6eb9a060e54bf8d69288fbee4904", targetHash)
		result.Commit, result.BaseCommitish, result.TargetCommitish = "Initial commit..."+short(targetHash), "", short(targetHash)
		raw, err := git(repo, args...)
		if err != nil {
			return Response{}, err
		}
		result.Files = ParseUnified(raw)
		result.IsEmpty = len(result.Files) == 0
		return result, nil
	}
	switch target {
	case "working":
		result.Commit, result.BaseCommitish, result.TargetCommitish = "Working Directory (unstaged changes)", base, target
	case "staged":
		baseHash, err := rev(repo, base)
		if err != nil {
			return Response{}, err
		}
		args = append(args, "--cached", base)
		result.Commit, result.BaseCommitish, result.TargetCommitish = short(baseHash)+" vs Staging Area (staged changes)", short(baseHash), target
	case ".":
		baseHash, err := rev(repo, base)
		if err != nil {
			return Response{}, err
		}
		args = append(args, base)
		result.Commit, result.BaseCommitish, result.TargetCommitish = short(baseHash)+" vs Working Directory (all uncommitted changes)", short(baseHash), target
	default:
		baseHash, err := rev(repo, base)
		if err != nil {
			return Response{}, err
		}
		targetHash, err := rev(repo, target)
		if err != nil {
			return Response{}, err
		}
		args = append(args, baseHash, targetHash)
		result.Commit, result.BaseCommitish, result.TargetCommitish = short(baseHash)+"..."+short(targetHash), short(baseHash), short(targetHash)
	}

	raw, err := git(repo, args...)
	if err != nil {
		return Response{}, err
	}
	result.Files = ParseUnified(raw)
	// Git's ordinary diff deliberately omits untracked files.  A review of a
	// dirty workspace must not hide those files, though: reviewers need the
	// same staged, unstaged, and untracked inventory that `git status` shows.
	// Only working-tree selections include them; a staged or commit-to-commit
	// comparison has no untracked-file meaning.
	if target == "." || target == "working" {
		untracked, untrackedErr := parseUntracked(repo)
		if untrackedErr != nil {
			return Response{}, untrackedErr
		}
		result.Files = append(result.Files, untracked...)
	}
	result.IsEmpty = len(result.Files) == 0
	return result, nil
}

// parseUntracked projects Git's ignored-aware untracked inventory into the
// same added-file shape returned by ParseUnified.  NUL-separated paths keep
// spaces and newlines unambiguous.  This is intentionally separate from
// ParseUnified: there is no Git unified-diff record for an untracked file.
func parseUntracked(repo string) ([]File, error) {
	raw, err := git(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return []File{}, nil
	}
	paths := strings.Split(raw, "\x00")
	files := make([]File, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		// Git reports an untracked nested repository as a directory marker
		// (for example "nested/") rather than walking into its .git metadata.
		// The daemon discovers that repository separately, so attempting to read
		// the marker as a file must not discard the parent repository's entire
		// diff. Ordinary untracked directories are already expanded to their
		// files by ls-files; empty directories have no diff representation.
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, fmt.Errorf("stat untracked %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		contents, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read untracked %s: %w", path, err)
		}
		files = append(files, untrackedFile(path, contents))
	}
	return files, nil
}

func untrackedFile(path string, contents []byte) File {
	text := strings.TrimSuffix(string(contents), "\n")
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	chunk := Chunk{
		Header:   fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines)),
		OldStart: 0,
		OldLines: 0,
		NewStart: 1,
		NewLines: len(lines),
		Lines:    make([]Line, 0, len(lines)),
	}
	for index, line := range lines {
		lineNumber := index + 1
		chunk.Lines = append(chunk.Lines, Line{Type: "add", Content: line, NewLineNumber: &lineNumber})
	}
	return File{Path: path, Status: "added", Additions: len(lines), Chunks: []Chunk{chunk}}
}

// ParseUnified parses Git's --no-ext-diff, --color=never unified output.
func ParseUnified(raw string) []File {
	if raw == "" {
		return []File{}
	}
	blocks := strings.Split(raw, "diff --git ")
	files := make([]File, 0, len(blocks)-1)
	for _, body := range blocks[1:] {
		if f, ok := parseFile("diff --git " + body); ok {
			files = append(files, f)
		}
	}
	return files
}

func pathFromHeader(line, prefix string) string {
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	p := strings.SplitN(strings.TrimPrefix(line, prefix), "\t", 2)[0]
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		p = p[2:]
	}
	return unquoteGitPath(p)
}

func unquoteGitPath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if q, err := strconv.Unquote(p); err == nil {
			return q
		}
	}
	return p
}

func parseHeaderPaths(line string) (string, string) {
	raw := strings.TrimPrefix(line, "diff --git ")
	fields := splitQuoted(raw)
	if len(fields) != 2 {
		return "", ""
	}
	return pathFromHeader("--- "+fields[0], "--- "), pathFromHeader("+++ "+fields[1], "+++ ")
}

func splitQuoted(s string) []string {
	var out []string
	var b strings.Builder
	quoted, escaped := false, false
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			b.WriteRune(r)
			continue
		}
		if r == ' ' && !quoted {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func parseFile(block string) (File, bool) {
	lines := strings.Split(block, "\n")
	if len(lines) == 0 {
		return File{}, false
	}
	headerOld, headerNew := parseHeaderPaths(lines[0])
	var minus, plus, renameFrom, renameTo string
	added, deleted := false, false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- "):
			minus = pathFromHeader(line, "--- ")
		case strings.HasPrefix(line, "+++ "):
			plus = pathFromHeader(line, "+++ ")
		case strings.HasPrefix(line, "rename from "):
			renameFrom = unquoteGitPath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			renameTo = unquoteGitPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "new file mode "):
			added = true
		case strings.HasPrefix(line, "deleted file mode "):
			deleted = true
		}
	}
	old, new := first(renameFrom, minus, headerOld), first(renameTo, plus, headerNew)
	if new == "" {
		new = old
	}
	if new == "" {
		return File{}, false
	}
	status := "modified"
	if added || minus == "" && strings.Contains(block, "--- /dev/null") {
		status = "added"
	} else if deleted || plus == "" && strings.Contains(block, "+++ /dev/null") {
		status = "deleted"
	} else if old != "" && old != new {
		status = "renamed"
	}
	chunks := parseChunks(lines)
	f := File{Path: new, Status: status, Chunks: chunks}
	if status == "renamed" {
		f.OldPath = &old
	}
	for _, c := range chunks {
		for _, l := range c.Lines {
			if l.Type == "add" {
				f.Additions++
			}
			if l.Type == "delete" {
				f.Deletions++
			}
		}
	}
	return f, true
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseChunks(lines []string) []Chunk {
	var chunks []Chunk
	var current *Chunk
	oldNum, newNum := 0, 0
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if current != nil {
				chunks = append(chunks, *current)
			}
			oldStart, oldLines, newStart, newLines, ok := parseHunkHeader(line)
			if !ok {
				current = nil
				continue
			}
			current = &Chunk{Header: line, OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}
			oldNum, newNum = oldStart, newStart
			continue
		}
		if current == nil || line == "" {
			continue
		}
		kind := ""
		switch line[0] {
		case '+':
			kind = "add"
		case '-':
			kind = "delete"
		case ' ':
			kind = "normal"
		default:
			continue
		}
		l := Line{Type: kind, Content: line[1:]}
		if kind != "add" {
			n := oldNum
			l.OldLineNumber = &n
			oldNum++
		}
		if kind != "delete" {
			n := newNum
			l.NewLineNumber = &n
			newNum++
		}
		current.Lines = append(current.Lines, l)
	}
	if current != nil {
		chunks = append(chunks, *current)
	}
	return chunks
}

func parseHunkHeader(s string) (int, int, int, int, bool) {
	// @@ -oldStart[,oldCount] +newStart[,newCount] @@ optional heading
	parts := strings.Fields(s)
	if len(parts) < 3 || parts[0] != "@@" {
		return 0, 0, 0, 0, false
	}
	oldStart, oldLines, ok := hunkRange(strings.TrimPrefix(parts[1], "-"))
	if !ok {
		return 0, 0, 0, 0, false
	}
	newStart, newLines, ok := hunkRange(strings.TrimPrefix(parts[2], "+"))
	if !ok {
		return 0, 0, 0, 0, false
	}
	return oldStart, oldLines, newStart, newLines, true
}
func hunkRange(s string) (int, int, bool) {
	p := strings.SplitN(s, ",", 2)
	start, err := strconv.Atoi(p[0])
	if err != nil {
		return 0, 0, false
	}
	count := 1
	if len(p) == 2 {
		count, err = strconv.Atoi(p[1])
		if err != nil {
			return 0, 0, false
		}
	}
	return start, count, true
}
