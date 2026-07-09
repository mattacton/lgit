package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lgit/internal/git"
	"lgit/internal/store"
)

// MergeResult reports what MergePR did.
type MergeResult struct {
	PR        *store.PR
	NewTip    string
	CleanedUp bool
	// CleanupSkipped is the reason cleanup was intentionally not attempted
	// (lgit's git context is rooted inside the PR's worktree).
	CleanupSkipped string
	// CleanupErr is set when cleanup was attempted and failed. The merge
	// itself succeeded in that case.
	CleanupErr error
}

// MergePR integrates a PR into its base branch. It is shared by the CLI and
// the HTTP API so both surfaces behave identically: guard against
// re-merging, refresh the diff range, run the git integration, set
// state=merged, then optionally remove the PR's worktree and branch.
// Cleanup problems never fail the call — the merge has already happened —
// they are reported in the result instead.
func (s *Server) MergePR(pr *store.PR, mode git.MergeMode, cleanup bool) (*MergeResult, error) {
	if pr.State == "merged" {
		return nil, fmt.Errorf("PR #%d is already merged", pr.Number)
	}
	pr = s.RefreshPR(pr)

	msg := fmt.Sprintf("%s (#%d)", pr.Title, pr.Number)
	if mode == git.MergeMerge {
		msg = fmt.Sprintf("Merge PR #%d: %s", pr.Number, pr.Title)
	}
	newTip, err := s.Repo.Merge(pr.BaseRef, pr.HeadRef, mode, msg)
	if err != nil {
		return nil, err
	}
	if err := s.Store.UpdatePRState(pr.ID, "merged"); err != nil {
		return nil, err
	}

	res := &MergeResult{NewTip: newTip}
	if cleanup {
		if pathInside(s.Repo.Dir, pr.WorktreePath) {
			res.CleanupSkipped = fmt.Sprintf(
				"cleanup skipped: lgit's git context is rooted inside the PR worktree %s; run `git worktree remove %s && git branch -D %s` from elsewhere, or `lgit sweep`",
				pr.WorktreePath, pr.WorktreePath, pr.HeadRef)
		} else if err := s.Repo.Cleanup(pr.WorktreePath, pr.HeadRef); err != nil {
			res.CleanupErr = err
		} else {
			res.CleanedUp = true
		}
	}

	fresh, err := s.Store.GetPR(pr.ID)
	if err != nil {
		return nil, err
	}
	res.PR = fresh
	return res, nil
}

// canonPath resolves symlinks (macOS tempdirs: /var vs /private/var) so path
// comparisons don't false-negative; falls back to Clean when the path is gone.
func canonPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// pathInside reports whether dir is path itself or a descendant of it,
// on path-segment boundaries (so /a/lgit-x is not inside /a/lgit).
func pathInside(dir, path string) bool {
	if path == "" {
		return false
	}
	d, p := canonPath(dir), canonPath(path)
	return d == p || strings.HasPrefix(d, p+string(os.PathSeparator))
}

// SweepEntry describes what Sweep did (or could not do) for one merged PR.
type SweepEntry struct {
	PR      store.PR `json:"pr"`
	Actions []string `json:"actions"`
	Err     error    `json:"-"`
}

// Sweep finishes cleanup for merged PRs that still have a worktree directory
// or head branch lying around (e.g. merges done without cleanup, or cleanups
// skipped by the worktree guard). PRs with nothing left to remove are not
// reported. The primary worktree is never removed, and a branch that is
// still checked out somewhere is kept.
func (s *Server) Sweep() ([]SweepEntry, error) {
	prs, err := s.Store.ListPRs("merged")
	if err != nil {
		return nil, err
	}
	primary, err := s.Repo.PrimaryWorktree()
	if err != nil {
		return nil, err
	}
	var entries []SweepEntry
	for _, pr := range prs {
		wtExists := false
		if pr.WorktreePath != "" && canonPath(pr.WorktreePath) != canonPath(primary) {
			if _, err := os.Stat(pr.WorktreePath); err == nil {
				wtExists = true
			}
		}
		branchExists := s.Repo.BranchExists(pr.HeadRef)
		if !wtExists && !branchExists {
			continue
		}
		e := SweepEntry{PR: pr}
		if wtExists {
			if pathInside(s.Repo.Dir, pr.WorktreePath) {
				e.Actions = append(e.Actions, fmt.Sprintf("kept worktree %s (lgit's git context is rooted inside it)", pr.WorktreePath))
			} else if err := s.Repo.RemoveWorktree(pr.WorktreePath); err != nil {
				e.Err = err
			} else {
				e.Actions = append(e.Actions, "removed worktree "+pr.WorktreePath)
			}
		}
		if branchExists && e.Err == nil {
			if wt, err := s.Repo.WorktreeForBranch(pr.HeadRef); err == nil && wt != nil {
				e.Actions = append(e.Actions, fmt.Sprintf("kept branch %s (checked out at %s)", pr.HeadRef, wt.Path))
			} else if err := s.Repo.DeleteBranch(pr.HeadRef); err != nil {
				e.Err = err
			} else {
				e.Actions = append(e.Actions, "deleted branch "+pr.HeadRef)
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

type mergeReq struct {
	Cleanup *bool `json:"cleanup"`
}

// handleMergePR merges a PR with the default mode (rebase). The UI's cleanup
// checkbox maps to the request's "cleanup" field; absent means true.
func (s *Server) handleMergePR(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	cleanup := true
	var req mergeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Cleanup != nil {
		cleanup = *req.Cleanup
	}
	res, err := s.MergePR(pr, git.MergeRebase, cleanup)
	if err != nil {
		// Already merged, or the integration failed (conflicts): the PR
		// can't be merged in its current state.
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	cleanupMsg := res.CleanupSkipped
	if res.CleanupErr != nil {
		cleanupMsg = "merge succeeded but cleanup failed: " + res.CleanupErr.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pr":              res.PR,
		"new_tip":         res.NewTip,
		"cleaned_up":      res.CleanedUp,
		"cleanup_message": cleanupMsg,
	})
}
