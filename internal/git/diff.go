package git

import (
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
