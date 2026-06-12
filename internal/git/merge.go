package git

import (
	"fmt"
	"os"
	"path/filepath"
)

// MergeMode selects how a PR head is integrated into its base branch.
type MergeMode string

const (
	MergeRebase MergeMode = "rebase"
	MergeSquash MergeMode = "squash"
	MergeMerge  MergeMode = "merge"
)

// Merge integrates headRef into baseRef using mode, without disturbing any
// existing worktree's working tree. It works in a temporary detached worktree,
// computes the new base tip there, then advances the base branch ref:
//
//   - if baseRef is checked out in a worktree, that worktree must be clean and
//     is fast-forwarded in place (ref + working tree move together);
//   - otherwise the ref is moved with `git branch -f`.
//
// commitMsg is used for the squash commit / merge commit. Returns the new tip
// SHA of baseRef.
func (r *Repo) Merge(baseRef, headRef string, mode MergeMode, commitMsg string) (string, error) {
	baseSHA, err := r.RevParse(baseRef)
	if err != nil {
		return "", fmt.Errorf("resolving base %q: %w", baseRef, err)
	}
	headSHA, err := r.RevParse(headRef)
	if err != nil {
		return "", fmt.Errorf("resolving head %q: %w", headRef, err)
	}

	tmpDir, err := os.MkdirTemp("", "lgit-merge-*")
	if err != nil {
		return "", err
	}
	tmpWT := filepath.Join(tmpDir, "wt")
	defer os.RemoveAll(tmpDir)

	// Start the temp worktree detached at the appropriate commit for the mode.
	startAt := baseSHA
	if mode == MergeRebase {
		startAt = headSHA
	}
	if _, err := r.run("worktree", "add", "--detach", tmpWT, startAt); err != nil {
		return "", fmt.Errorf("creating temp worktree: %w", err)
	}
	defer r.run("worktree", "remove", "--force", tmpWT)

	wt := &Repo{Dir: tmpWT}
	switch mode {
	case MergeRebase:
		if _, err := wt.run("rebase", baseSHA); err != nil {
			wt.run("rebase", "--abort")
			return "", fmt.Errorf("rebase onto %s failed (conflicts?): %w", baseRef, err)
		}
	case MergeSquash:
		if _, err := wt.run("merge", "--squash", headSHA); err != nil {
			wt.run("merge", "--abort")
			return "", fmt.Errorf("squash merge failed (conflicts?): %w", err)
		}
		if _, err := wt.run("commit", "-m", commitMsg); err != nil {
			return "", fmt.Errorf("squash commit failed: %w", err)
		}
	case MergeMerge:
		if _, err := wt.run("merge", "--no-ff", "-m", commitMsg, headSHA); err != nil {
			wt.run("merge", "--abort")
			return "", fmt.Errorf("merge failed (conflicts?): %w", err)
		}
	default:
		return "", fmt.Errorf("unknown merge mode %q", mode)
	}

	newTip, err := wt.RevParse("HEAD")
	if err != nil {
		return "", err
	}

	// Advance the base branch ref.
	baseWT, err := r.WorktreeForBranch(baseRef)
	if err != nil {
		return "", err
	}
	if baseWT != nil {
		clean, err := r.IsClean(baseWT.Path)
		if err != nil {
			return "", err
		}
		if !clean {
			return "", fmt.Errorf("branch %q is checked out in %s which has uncommitted changes; commit or stash them first", baseRef, baseWT.Path)
		}
		baseRepo := &Repo{Dir: baseWT.Path}
		if _, err := baseRepo.run("merge", "--ff-only", newTip); err != nil {
			return "", fmt.Errorf("fast-forwarding %s in %s: %w", baseRef, baseWT.Path, err)
		}
	} else {
		if _, err := r.run("branch", "-f", baseRef, newTip); err != nil {
			return "", fmt.Errorf("moving branch %s: %w", baseRef, err)
		}
	}
	return newTip, nil
}

// Cleanup removes the PR's worktree and deletes its head branch. The branch
// is deleted with -D because rebase/squash integration rewrites commits, so
// git cannot see the branch as merged.
func (r *Repo) Cleanup(worktreePath, headRef string) error {
	if worktreePath != "" {
		if _, err := os.Stat(worktreePath); err == nil {
			if _, err := r.run("worktree", "remove", worktreePath); err != nil {
				return fmt.Errorf("removing worktree: %w", err)
			}
		} else {
			// Worktree dir already gone; prune stale metadata.
			r.run("worktree", "prune")
		}
	}
	if _, err := r.run("branch", "-D", headRef); err != nil {
		return fmt.Errorf("deleting branch %s: %w", headRef, err)
	}
	return nil
}
