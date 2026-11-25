// Package server implements lgit's local HTTP server: a JSON API over the
// store/git layers plus the embedded built web UI.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"

	"lgit/internal/git"
	"lgit/internal/store"
)

//go:embed all:dist
var distFS embed.FS

// Server holds the dependencies for the HTTP API.
type Server struct {
	Store *store.Store
	Repo  *git.Repo
}

// New constructs a Server.
func New(st *store.Store, repo *git.Repo) *Server {
	return &Server{Store: st, Repo: repo}
}

// Handler returns the root http.Handler (API + static UI).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/branches", s.handleListBranches)
	mux.HandleFunc("GET /api/prs", s.handleListPRs)
	mux.HandleFunc("GET /api/prs/{number}", s.handleGetPR)
	mux.HandleFunc("POST /api/prs/{number}/base", s.handleSetBase)
	mux.HandleFunc("GET /api/prs/{number}/diff", s.handleGetDiff)
	mux.HandleFunc("GET /api/prs/{number}/threads", s.handleListThreads)
	mux.HandleFunc("POST /api/prs/{number}/threads", s.handleCreateThread)
	mux.HandleFunc("POST /api/prs/{number}/reviews", s.handleCreateReview)
	mux.HandleFunc("POST /api/prs/{number}/merge", s.handleMergePR)
	mux.HandleFunc("POST /api/threads/{id}/comments", s.handleAddComment)
	mux.HandleFunc("POST /api/threads/{id}/resolve", s.handleResolveThread)

	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServerFS(dist)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := dist.Open(path); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: serve index.html for client-routed paths.
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "UI assets not embedded; rebuild with `npm run build` in web/", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	return mux
}

// ListenAndServe starts the server on the given port.
func (s *Server) ListenAndServe(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("lgit serving on http://%s", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func (s *Server) prFromPath(r *http.Request) (*store.PR, error) {
	n, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bad PR number: %w", store.ErrNotFound)
	}
	return s.Store.GetPRByNumber(n)
}

// RefreshPR recomputes a live PR's diff range (base/merge-base/head SHAs) so
// new commits on the head branch appear. Merged/closed PRs are left frozen.
// Failures (e.g. branch deleted) are non-fatal; stored values stay.
func (s *Server) RefreshPR(pr *store.PR) *store.PR {
	if pr.State == "merged" || pr.State == "closed" {
		return pr
	}
	headSHA, err1 := s.Repo.RevParse(pr.HeadRef)
	baseSHA, err2 := s.Repo.RevParse(pr.BaseRef)
	if err1 != nil || err2 != nil {
		return pr
	}
	mergeBase, err := s.Repo.MergeBase(pr.BaseRef, pr.HeadRef)
	if err != nil {
		return pr
	}
	headChanged := headSHA != pr.HeadSHA
	if headChanged || baseSHA != pr.BaseSHA || mergeBase != pr.MergeBaseSHA {
		if err := s.Store.UpdatePRRange(pr.ID, baseSHA, mergeBase, headSHA); err == nil {
			if fresh, err := s.Store.GetPR(pr.ID); err == nil {
				if headChanged {
					s.RefreshOutdatedThreads(fresh)
				}
				return fresh
			}
		}
	}
	return pr
}

// RefreshOutdatedThreads updates unresolved inline thread stale badges against
// the PR's current head. It is best-effort so a missing branch/file does not
// block the rest of PR refresh.
func (s *Server) RefreshOutdatedThreads(pr *store.PR) {
	threads, err := s.Store.ListThreads(pr.ID, true)
	if err != nil {
		return
	}
	for _, th := range threads {
		if th.FilePath == "" || th.AnchorSHA == "" || th.AnchorSHA == pr.HeadSHA {
			_ = s.Store.SetThreadOutdated(th.ID, false)
			continue
		}
		changed, err := s.Repo.LineChanged(th.AnchorSHA, pr.HeadSHA, th.FilePath, th.Line)
		if err != nil {
			continue
		}
		_ = s.Store.SetThreadOutdated(th.ID, changed)
	}
}

// --- handlers ---

func (s *Server) handleListPRs(w http.ResponseWriter, r *http.Request) {
	prs, err := s.Store.ListPRs(r.URL.Query().Get("state"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if prs == nil {
		prs = []store.PR{}
	}
	writeJSON(w, http.StatusOK, prs)
}

func (s *Server) handleGetPR(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	pr = s.RefreshPR(pr)
	writeJSON(w, http.StatusOK, pr)
}

func (s *Server) handleGetDiff(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	pr = s.RefreshPR(pr)
	fromSHA := pr.MergeBaseSHA
	var lastReview *store.Review
	if r.URL.Query().Get("since") == "last-review" {
		lastReview, err = s.Store.LastReview(pr.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		if lastReview != nil && lastReview.HeadSHA != "" {
			fromSHA = lastReview.HeadSHA
		}
	}
	files, err := s.Repo.Diff(fromSHA, pr.HeadSHA)
	if err != nil {
		writeErr(w, err)
		return
	}
	if files == nil {
		files = []git.FileDiff{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pr": pr, "files": files, "from_sha": fromSHA, "to_sha": pr.HeadSHA, "last_review": lastReview,
	})
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	pr = s.RefreshPR(pr)
	unresolved := r.URL.Query().Get("unresolved") == "true"
	threads, err := s.Store.ListThreads(pr.ID, unresolved)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

type createThreadReq struct {
	FilePath string `json:"file_path"`
	Line     int64  `json:"line"`
	Side     string `json:"side"`
	Body     string `json:"body"`
	Author   string `json:"author"`
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req createThreadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		badRequest(w, "body is required")
		return
	}
	if req.Side == "" {
		req.Side = "new"
	}
	if req.Side != "old" && req.Side != "new" {
		badRequest(w, "side must be old or new")
		return
	}
	if req.Author == "" {
		req.Author = "user"
	}
	if req.Author != "user" && req.Author != "agent" {
		badRequest(w, "author must be user or agent")
		return
	}
	th, err := s.Store.CreateThread(store.Thread{
		PRID: pr.ID, FilePath: req.FilePath, Line: req.Line,
		Side: req.Side, AnchorSHA: pr.HeadSHA,
	}, req.Author, req.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, th)
}

type createReviewReq struct {
	Verdict string `json:"verdict"`
	Body    string `json:"body"`
}

// ErrBadVerdict is returned by SubmitReview for an unknown verdict.
var ErrBadVerdict = errors.New("verdict must be approve, request_changes, or comment")

// SubmitReview records a review at the current PR head and updates state for
// approve / request_changes unless the PR is already merged or closed.
func (s *Server) SubmitReview(pr *store.PR, verdict, body string) (*store.Review, *store.PR, error) {
	pr = s.RefreshPR(pr)
	var newState string
	switch verdict {
	case "approve":
		newState = "approved"
	case "request_changes":
		newState = "changes_requested"
	case "comment":
	default:
		return nil, nil, ErrBadVerdict
	}
	rev, err := s.Store.CreateReview(pr.ID, verdict, body, pr.HeadSHA)
	if err != nil {
		return nil, nil, err
	}
	if newState != "" && pr.State != "merged" && pr.State != "closed" {
		if err := s.Store.UpdatePRState(pr.ID, newState); err != nil {
			return nil, nil, err
		}
	}
	fresh, err := s.Store.GetPR(pr.ID)
	if err != nil {
		return nil, nil, err
	}
	return rev, fresh, nil
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	pr, err := s.prFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req createReviewReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid JSON: "+err.Error())
		return
	}
	rev, fresh, err := s.SubmitReview(pr, req.Verdict, req.Body)
	if err != nil {
		if errors.Is(err, ErrBadVerdict) {
			badRequest(w, err.Error())
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"review": rev, "pr": fresh})
}

type addCommentReq struct {
	Body   string `json:"body"`
	Author string `json:"author"`
}

func threadIDFromPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad thread id: %w", store.ErrNotFound)
	}
	return id, nil
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	id, err := threadIDFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req addCommentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		badRequest(w, "body is required")
		return
	}
	if req.Author == "" {
		req.Author = "user"
	}
	if req.Author != "user" && req.Author != "agent" {
		badRequest(w, "author must be user or agent")
		return
	}
	c, err := s.Store.AddComment(id, req.Author, req.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

type resolveReq struct {
	Resolved *bool `json:"resolved"`
}

func (s *Server) handleResolveThread(w http.ResponseWriter, r *http.Request) {
	id, err := threadIDFromPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	resolved := true
	var req resolveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Resolved != nil {
		resolved = *req.Resolved
	}
	if err := s.Store.SetThreadResolved(id, resolved); err != nil {
		writeErr(w, err)
		return
	}
	th, err := s.Store.GetThread(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}
