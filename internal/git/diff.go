package git

import (
	"fmt"
	"strconv"
	"strings"
)

// FileDiff is one file's diff within a PR's diff range, in a form the
// frontend and JSON API can consume directly.
type FileDiff struct {
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Status    string `json:"status"` // added, deleted, modified, renamed
	Binary    bool   `json:"binary"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Patch is the per-file unified diff, including the `diff --git` header,
	// suitable for parsing with react-diff-view's parseDiff.
	Patch string `json:"patch"`
}

// Diff computes the diff between two revs (typically merge_base..head) and
// returns it split per file.
func (r *Repo) Diff(from, to string) ([]FileDiff, error) {
	raw, err := r.runRaw("diff", "--find-renames", from+".."+to, "--")
	if err != nil {
		return nil, err
	}
	return parseDiff(raw), nil
}

// LineChanged reports whether line in path at from falls within a changed hunk
// between from and to. It is used to flag inline review threads as outdated
// after new commits land on a PR head.
func (r *Repo) LineChanged(from, to, path string, line int64) (bool, error) {
	if path == "" || line <= 0 || from == to {
		return false, nil
	}
	raw, err := r.runRaw("diff", "--unified=0", from+".."+to, "--", path)
	if err != nil {
		return false, err
	}
	for l := range strings.Lines(raw) {
		l = strings.TrimRight(l, "\n")
		if !strings.HasPrefix(l, "@@ ") {
			continue
		}
		start, count, err := parseOldHunkRange(l)
		if err != nil {
			return false, err
		}
		if lineInRange(line, start, count) {
			return true, nil
		}
	}
	return false, nil
}

func parseOldHunkRange(header string) (int64, int64, error) {
	fields := strings.Fields(header)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "-") {
		return 0, 0, fmt.Errorf("bad hunk header: %q", header)
	}
	rangePart := strings.TrimPrefix(fields[1], "-")
	startPart, countPart, ok := strings.Cut(rangePart, ",")
	start, err := strconv.ParseInt(startPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad hunk start in %q: %w", header, err)
	}
	if !ok {
		return start, 1, nil
	}
	count, err := strconv.ParseInt(countPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad hunk count in %q: %w", header, err)
	}
	return start, count, nil
}

func lineInRange(line, start, count int64) bool {
	if count == 0 {
		return line == start
	}
	return line >= start && line < start+count
}

// parseDiff splits full `git diff` output into per-file diffs and extracts
// metadata from each chunk's headers.
func parseDiff(raw string) []FileDiff {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var files []FileDiff
	var cur *FileDiff
	var buf strings.Builder

	flush := func() {
		if cur != nil {
			cur.Patch = buf.String()
			files = append(files, *cur)
		}
		buf.Reset()
	}

	for line := range strings.Lines(raw) {
		trimmed := strings.TrimRight(line, "\n")
		if strings.HasPrefix(trimmed, "diff --git ") {
			flush()
			cur = &FileDiff{Status: "modified"}
			cur.OldPath, cur.NewPath = parseDiffGitLine(trimmed)
		}
		if cur == nil {
			continue
		}
		buf.WriteString(line)
		switch {
		case strings.HasPrefix(trimmed, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(trimmed, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(trimmed, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = strings.TrimPrefix(trimmed, "rename from ")
		case strings.HasPrefix(trimmed, "rename to "):
			cur.NewPath = strings.TrimPrefix(trimmed, "rename to ")
		case strings.HasPrefix(trimmed, "Binary files ") || strings.HasPrefix(trimmed, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(trimmed, "--- a/"):
			cur.OldPath = strings.TrimPrefix(trimmed, "--- a/")
		case strings.HasPrefix(trimmed, "+++ b/"):
			cur.NewPath = strings.TrimPrefix(trimmed, "+++ b/")
		case strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++"):
			cur.Additions++
		case strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---"):
			cur.Deletions++
		}
	}
	flush()

	for i := range files {
		if files[i].Status == "added" {
			files[i].OldPath = ""
		}
		if files[i].Status == "deleted" {
			files[i].NewPath = ""
		}
	}
	return files
}

// parseDiffGitLine extracts old/new paths from a `diff --git a/x b/y` line.
// Paths with spaces are handled by git's quoting only in exotic cases; for
// the common case we split on " b/".
func parseDiffGitLine(line string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.Index(rest, " b/"); i >= 0 && strings.HasPrefix(rest, "a/") {
		return rest[2:i], rest[i+3:]
	}
	return "", ""
}
