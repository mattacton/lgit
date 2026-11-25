// Package git wraps the git CLI (via os/exec) with the operations lgit needs:
// worktree discovery, merge-base/diff-range computation, and merge integration.
package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo represents a git repository context rooted at a particular directory
// (typically the worktree lgit was invoked from).
type Repo struct {
	// Dir is the directory git commands run in.
	Dir string
}

// Open returns a Repo for the given directory, verifying it is inside a git
// working tree.
func Open(dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	if _, err := r.run("rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("not inside a git work tree: %w", err)
	}
	return r, nil
}

// run executes git with the given args in r.Dir and returns trimmed stdout.
func (r *Repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// runRaw is like run but returns stdout untrimmed and keeps stderr separate.
func (r *Repo) runRaw(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// CommonDir returns the absolute path of the repository's common git dir
// (shared by all worktrees).
func (r *Repo) CommonDir() (string, error) {
	out, err := r.run("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		top, err := r.Toplevel()
		if err != nil {
			return "", err
		}
		out = filepath.Join(top, out)
	}
	return filepath.Clean(out), nil
}

// Toplevel returns the absolute path of the current worktree's top-level dir.
func (r *Repo) Toplevel() (string, error) {
	return r.run("rev-parse", "--show-toplevel")
}

// CurrentBranch returns the current branch name, or an error if HEAD is detached.
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", fmt.Errorf("HEAD is detached; lgit needs a branch")
	}
	return out, nil
}

// RevParse resolves a rev to a full SHA.
func (r *Repo) RevParse(rev string) (string, error) {
	return r.run("rev-parse", "--verify", rev+"^{commit}")
}

// MergeBase returns the merge base of two revs.
func (r *Repo) MergeBase(a, b string) (string, error) {
	return r.run("merge-base", a, b)
}

// SwitchDetach checks out rev on a detached HEAD, leaving all branch refs
// untouched (works even when rev's branch is checked out in another worktree).
func (r *Repo) SwitchDetach(rev string) error {
	_, err := r.run("switch", "--detach", rev)
	return err
}

// Worktree describes one entry from `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // full ref, e.g. refs/heads/main; empty when detached
	Detached bool
}

// Worktrees lists all worktrees of the repository.
func (r *Repo) Worktrees() ([]Worktree, error) {
	out, err := r.runRaw("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	var cur *Worktree
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				wts = append(wts, *cur)
			}
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			if cur != nil {
				cur.Head = strings.TrimPrefix(line, "HEAD ")
			}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				cur.Branch = strings.TrimPrefix(line, "branch ")
			}
		case line == "detached":
			if cur != nil {
				cur.Detached = true
			}
		}
	}
	if cur != nil {
		wts = append(wts, *cur)
	}
	return wts, nil
}

// PrimaryWorktree returns the path of the repository's main worktree (git
// lists it first). Long-running processes re-root themselves there so their
// git context never depends on the directory they were launched from.
func (r *Repo) PrimaryWorktree() (string, error) {
	wts, err := r.Worktrees()
	if err != nil {
		return "", err
	}
	if len(wts) == 0 {
		return "", fmt.Errorf("no worktrees found")
	}
	return wts[0].Path, nil
}

// BranchExists reports whether the local branch exists.
func (r *Repo) BranchExists(name string) bool {
	_, err := r.run("rev-parse", "--verify", "refs/heads/"+name)
	return err == nil
}

// LocalBranches returns local branch names (refs/heads), sorted by refname.
func (r *Repo) LocalBranches() ([]string, error) {
	out, err := r.run("for-each-ref", "--format=%(refname:short)", "--sort=refname", "refs/heads")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	var names []string
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\n")
		if line != "" {
			names = append(names, line)
		}
	}
	if names == nil {
		return []string{}, nil
	}
	return names, nil
}

// WorktreeForBranch returns the worktree that has the given branch checked
// out, or nil if none does.
func (r *Repo) WorktreeForBranch(branch string) (*Worktree, error) {
	wts, err := r.Worktrees()
	if err != nil {
		return nil, err
	}
	ref := "refs/heads/" + branch
	for i := range wts {
		if wts[i].Branch == ref {
			return &wts[i], nil
		}
	}
	return nil, nil
}

// IsClean reports whether the worktree at dir has no uncommitted changes.
func (r *Repo) IsClean(dir string) (bool, error) {
	sub := &Repo{Dir: dir}
	out, err := sub.runRaw("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}
