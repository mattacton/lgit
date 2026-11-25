package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"lgit/internal/store"
)

// SetBaseResult is the outcome of retargeting a PR.
type SetBaseResult struct {
	PR              *store.PR `json:"pr"`
	PreviousBase    string    `json:"previous_base"`
	ApprovalCleared bool      `json:"approval_cleared"`
}

// SetBase changes a live PR's target branch and recomputes the diff range
// (base SHA, merge-base, head SHA). Merged and closed PRs are rejected.
// Retargeting an approved PR returns it to open: the approval was for a
// different merge target, and the diff against the new base needs review.
// Setting the base to its current value refreshes the range and does not
// clear an approval.
func (s *Server) SetBase(pr *store.PR, baseRef string) (*SetBaseResult, error) {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return nil, fmt.Errorf("base branch is required")
	}
	if pr.State == "merged" || pr.State == "closed" {
		return nil, fmt.Errorf("PR #%d is %s; only an open PR can change its target branch", pr.Number, pr.State)
	}
	if baseRef == pr.BaseRef {
		return &SetBaseResult{PR: s.RefreshPR(pr), PreviousBase: pr.BaseRef}, nil
	}
	if baseRef == pr.HeadRef {
		return nil, fmt.Errorf("base branch %q is the PR head; pick a different target", baseRef)
	}
	if !s.Repo.BranchExists(baseRef) {
		return nil, fmt.Errorf("base branch %q does not exist", baseRef)
	}
	baseSHA, err := s.Repo.RevParse(baseRef)
	if err != nil {
		return nil, fmt.Errorf("resolving base %q: %w", baseRef, err)
	}
	headSHA, err := s.Repo.RevParse(pr.HeadRef)
	if err != nil {
		return nil, fmt.Errorf("resolving head %q: %w", pr.HeadRef, err)
	}
	mergeBase, err := s.Repo.MergeBase(baseRef, pr.HeadRef)
	if err != nil {
		return nil, fmt.Errorf("computing merge-base of %q and %q: %w", baseRef, pr.HeadRef, err)
	}
	if err := s.Store.UpdatePRBase(pr.ID, baseRef, baseSHA, mergeBase, headSHA); err != nil {
		return nil, err
	}
	cleared := false
	if pr.State == "approved" {
		if err := s.Store.UpdatePRState(pr.ID, "open"); err != nil {
			return nil, err
		}
		cleared = true
	}
	fresh, err := s.Store.GetPR(pr.ID)
	if err != nil {
		return nil, err
	}
	return &SetBaseResult{PR: fresh, PreviousBase: pr.BaseRef, ApprovalCleared: cleared}, nil
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	names, err := s.Repo.LocalBranches()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, names)
}

type setBaseReq struct {
	Base string `json:"base"`
}

func (s *Server) handleSetBase(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req setBaseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid JSON: "+err.Error())
		return
	}
	res, err := s.SetBase(pr, req.Base)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
