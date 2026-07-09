package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lgit/internal/git"
	"lgit/internal/store"
)

// TestMain isolates git from the developer's real configuration (commit
// signing, hooks, aliases) so tests behave the same on any machine and in CI.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Exit(m.Run())
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture wires a Server to a scratch git repo (main plus a feature branch
// one commit ahead) and a scratch store, mirroring what `lgit pr create`
// snapshots.
type fixture struct {
	t       *testing.T
	handler http.Handler
	store   *store.Store
	repoDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.name", "test")
	mustGit(t, dir, "config", "user.email", "test@test")
	writeFile(t, dir, "a.txt", "hello\nworld\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "initial")
	mustGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "a.txt", "hello\nbrave new\nworld\n")
	writeFile(t, dir, "b.go", "package main\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "feature work")
	mustGit(t, dir, "checkout", "main")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "lgit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fixture{t: t, handler: New(st, repo).Handler(), store: st, repoDir: dir}
}

// createPR snapshots main..feature into a PR row, like `lgit pr create`.
func (f *fixture) createPR() *store.PR {
	f.t.Helper()
	head := mustGit(f.t, f.repoDir, "rev-parse", "feature")
	base := mustGit(f.t, f.repoDir, "rev-parse", "main")
	mb := mustGit(f.t, f.repoDir, "merge-base", "main", "feature")
	pr, err := f.store.CreatePR(store.PR{
		Title: "test PR", BaseRef: "main", HeadRef: "feature",
		BaseSHA: base, MergeBaseSHA: mb, HeadSHA: head, WorktreePath: f.repoDir,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return pr
}

// commitOnFeature adds a commit to the feature branch and returns its SHA.
func (f *fixture) commitOnFeature(name, content string) string {
	f.t.Helper()
	mustGit(f.t, f.repoDir, "checkout", "feature")
	writeFile(f.t, f.repoDir, name, content)
	mustGit(f.t, f.repoDir, "add", ".")
	mustGit(f.t, f.repoDir, "commit", "-m", "more work")
	sha := mustGit(f.t, f.repoDir, "rev-parse", "feature")
	mustGit(f.t, f.repoDir, "checkout", "main")
	return sha
}

// do serves one request against the handler, JSON-encoding body when non-nil.
func (f *fixture) do(method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
	return rec
}

func TestListPRs(t *testing.T) {
	f := newFixture(t)

	rec := wantStatus(t, f.do("GET", "/api/prs", nil), http.StatusOK)
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty list body = %q, want []", got)
	}

	f.createPR()
	pr2 := f.createPR()
	if err := f.store.UpdatePRState(pr2.ID, "approved"); err != nil {
		t.Fatal(err)
	}

	prs := decode[[]store.PR](t, wantStatus(t, f.do("GET", "/api/prs", nil), http.StatusOK))
	if len(prs) != 2 || prs[0].Number != 2 || prs[1].Number != 1 {
		t.Errorf("list = %+v, want numbers [2 1]", prs)
	}

	prs = decode[[]store.PR](t, wantStatus(t, f.do("GET", "/api/prs?state=approved", nil), http.StatusOK))
	if len(prs) != 1 || prs[0].Number != 2 {
		t.Errorf("state filter = %+v, want just #2", prs)
	}
}

func TestGetPR(t *testing.T) {
	f := newFixture(t)
	created := f.createPR()

	got := decode[store.PR](t, wantStatus(t, f.do("GET", "/api/prs/1", nil), http.StatusOK))
	if got.Number != created.Number || got.Title != created.Title || got.HeadSHA != created.HeadSHA {
		t.Errorf("got %+v, want %+v", got, created)
	}

	wantStatus(t, f.do("GET", "/api/prs/99", nil), http.StatusNotFound)
	wantStatus(t, f.do("GET", "/api/prs/abc", nil), http.StatusNotFound)
}

func TestGetPRRefreshesRange(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	newHead := f.commitOnFeature("c.txt", "more\n")

	got := decode[store.PR](t, wantStatus(t, f.do("GET", "/api/prs/1", nil), http.StatusOK))
	if got.HeadSHA != newHead {
		t.Errorf("head_sha = %s, want refreshed %s", got.HeadSHA, newHead)
	}
	stored, err := f.store.GetPR(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HeadSHA != newHead {
		t.Errorf("stored head_sha = %s, want persisted %s", stored.HeadSHA, newHead)
	}
}

func TestMergedPRNotRefreshed(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	if err := f.store.UpdatePRState(pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	f.commitOnFeature("c.txt", "more\n")

	got := decode[store.PR](t, wantStatus(t, f.do("GET", "/api/prs/1", nil), http.StatusOK))
	if got.HeadSHA != pr.HeadSHA {
		t.Errorf("merged PR head_sha = %s, want frozen %s", got.HeadSHA, pr.HeadSHA)
	}
}

type diffResp struct {
	PR    store.PR       `json:"pr"`
	Files []git.FileDiff `json:"files"`
}

func TestGetDiff(t *testing.T) {
	f := newFixture(t)
	f.createPR()

	resp := decode[diffResp](t, wantStatus(t, f.do("GET", "/api/prs/1/diff", nil), http.StatusOK))
	if len(resp.Files) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(resp.Files), resp.Files)
	}
	byPath := map[string]git.FileDiff{}
	for _, fd := range resp.Files {
		byPath[fd.NewPath] = fd
	}
	if a := byPath["a.txt"]; a.Status != "modified" || !strings.Contains(a.Patch, "+brave new") {
		t.Errorf("a.txt: %+v", a)
	}
	if b := byPath["b.go"]; b.Status != "added" {
		t.Errorf("b.go: %+v", b)
	}

	wantStatus(t, f.do("GET", "/api/prs/99/diff", nil), http.StatusNotFound)
}

func TestCreateThread(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	path := fmt.Sprintf("/api/prs/%d/threads", pr.Number)

	rec := wantStatus(t, f.do("POST", path, map[string]any{
		"file_path": "a.txt", "line": 2, "body": "why brave?",
	}), http.StatusCreated)
	th := decode[store.Thread](t, rec)
	if th.Side != "new" || th.AnchorSHA != pr.HeadSHA {
		t.Errorf("defaults: side=%q anchor=%q, want new/%s", th.Side, th.AnchorSHA, pr.HeadSHA)
	}
	if len(th.Comments) != 1 || th.Comments[0].Author != "user" || th.Comments[0].Body != "why brave?" {
		t.Errorf("first comment: %+v", th.Comments)
	}

	wantStatus(t, f.do("POST", path, map[string]any{"body": "  "}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", path, map[string]any{"body": "x", "side": "left"}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", path, map[string]any{"body": "x", "author": "bot"}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", "/api/prs/99/threads", map[string]any{"body": "x"}), http.StatusNotFound)
}

func TestListThreads(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	path := fmt.Sprintf("/api/prs/%d/threads", pr.Number)

	th1 := decode[store.Thread](t, f.do("POST", path, map[string]any{
		"file_path": "a.txt", "line": 2, "body": "one",
	}))
	f.do("POST", path, map[string]any{"file_path": "b.go", "line": 1, "body": "two"})
	wantStatus(t, f.do("POST", fmt.Sprintf("/api/threads/%d/resolve", th1.ID), nil), http.StatusOK)

	all := decode[[]store.Thread](t, wantStatus(t, f.do("GET", path, nil), http.StatusOK))
	if len(all) != 2 {
		t.Fatalf("got %d threads, want 2", len(all))
	}
	unresolved := decode[[]store.Thread](t, wantStatus(t, f.do("GET", path+"?unresolved=true", nil), http.StatusOK))
	if len(unresolved) != 1 || unresolved[0].Comments[0].Body != "two" {
		t.Errorf("unresolved = %+v, want just thread two", unresolved)
	}
}

func TestAddComment(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	th := decode[store.Thread](t, f.do("POST", fmt.Sprintf("/api/prs/%d/threads", pr.Number),
		map[string]any{"file_path": "a.txt", "line": 2, "body": "fix this"}))
	path := fmt.Sprintf("/api/threads/%d/comments", th.ID)

	rec := wantStatus(t, f.do("POST", path, map[string]any{
		"body": "done in abc123", "author": "agent",
	}), http.StatusCreated)
	if c := decode[store.Comment](t, rec); c.Author != "agent" || c.Body != "done in abc123" {
		t.Errorf("comment: %+v", c)
	}
	got, err := f.store.GetThread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 2 {
		t.Errorf("thread has %d comments, want 2", len(got.Comments))
	}

	wantStatus(t, f.do("POST", path, map[string]any{"body": "  "}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", path, map[string]any{"body": "x", "author": "bot"}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", "/api/threads/999/comments", map[string]any{"body": "x"}), http.StatusNotFound)
}

func TestResolveUnresolve(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	th := decode[store.Thread](t, f.do("POST", fmt.Sprintf("/api/prs/%d/threads", pr.Number),
		map[string]any{"file_path": "a.txt", "line": 2, "body": "nit"}))
	path := fmt.Sprintf("/api/threads/%d/resolve", th.ID)

	got := decode[store.Thread](t, wantStatus(t, f.do("POST", path, nil), http.StatusOK))
	if !got.Resolved {
		t.Error("thread not resolved after resolve with empty body")
	}
	got = decode[store.Thread](t, wantStatus(t, f.do("POST", path, map[string]any{"resolved": false}), http.StatusOK))
	if got.Resolved {
		t.Error("thread still resolved after unresolve")
	}

	wantStatus(t, f.do("POST", "/api/threads/999/resolve", nil), http.StatusNotFound)
}

type reviewResp struct {
	Review store.Review `json:"review"`
	PR     store.PR     `json:"pr"`
}

func TestCreateReview(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	path := fmt.Sprintf("/api/prs/%d/reviews", pr.Number)

	resp := decode[reviewResp](t, wantStatus(t, f.do("POST", path, map[string]any{
		"verdict": "request_changes", "body": "needs work",
	}), http.StatusCreated))
	if resp.Review.Verdict != "request_changes" || resp.PR.State != "changes_requested" {
		t.Errorf("request_changes: review=%+v pr.state=%q", resp.Review, resp.PR.State)
	}

	resp = decode[reviewResp](t, f.do("POST", path, map[string]any{"verdict": "comment", "body": "note"}))
	if resp.PR.State != "changes_requested" {
		t.Errorf("comment verdict changed state to %q", resp.PR.State)
	}

	resp = decode[reviewResp](t, f.do("POST", path, map[string]any{"verdict": "approve"}))
	if resp.PR.State != "approved" {
		t.Errorf("approve: pr.state = %q", resp.PR.State)
	}

	wantStatus(t, f.do("POST", path, map[string]any{"verdict": "lgtm"}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", "/api/prs/99/reviews", map[string]any{"verdict": "approve"}), http.StatusNotFound)
}

func TestReviewDoesNotReopenMergedPR(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	if err := f.store.UpdatePRState(pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}

	resp := decode[reviewResp](t, wantStatus(t, f.do("POST", fmt.Sprintf("/api/prs/%d/reviews", pr.Number),
		map[string]any{"verdict": "approve"}), http.StatusCreated))
	if resp.PR.State != "merged" {
		t.Errorf("approve on merged PR set state to %q, want merged", resp.PR.State)
	}
}

func TestUIServedWithSPAFallback(t *testing.T) {
	f := newFixture(t)

	index := wantStatus(t, f.do("GET", "/", nil), http.StatusOK)
	if ct := index.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// Client-routed paths fall back to index.html.
	spa := wantStatus(t, f.do("GET", "/pr/1", nil), http.StatusOK)
	if !bytes.Equal(spa.Body.Bytes(), index.Body.Bytes()) {
		t.Error("SPA fallback did not serve index.html")
	}

	// Real embedded assets are served directly, not via the fallback.
	assets, err := fs.ReadDir(distFS, "dist/assets")
	if err != nil || len(assets) == 0 {
		t.Fatalf("embedded dist/assets missing: %v", err)
	}
	asset := wantStatus(t, f.do("GET", "/assets/"+assets[0].Name(), nil), http.StatusOK)
	if ct := asset.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Errorf("asset served as HTML fallback (Content-Type %q)", ct)
	}
}
