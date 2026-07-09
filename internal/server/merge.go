package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	// (the process cwd is inside the PR's worktree).
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
		if cwd, err := os.Getwd(); err == nil && strings.HasPrefix(cwd, pr.WorktreePath) {
			res.CleanupSkipped = fmt.Sprintf(
				"cleanup skipped: current directory is inside the PR worktree %s; run `git worktree remove %s && git branch -D %s` from elsewhere",
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
