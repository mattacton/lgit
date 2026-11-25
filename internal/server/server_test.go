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
	"slices"
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

func TestListBranches(t *testing.T) {
	f := newFixture(t)
	names := decode[[]string](t, wantStatus(t, f.do("GET", "/api/branches", nil), http.StatusOK))
	if !slices.Equal(names, []string{"feature", "main"}) {
		t.Errorf("branches = %v, want [feature main]", names)
	}
}

func TestSetBase(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	mustGit(t, f.repoDir, "checkout", "-b", "other")
	writeFile(t, f.repoDir, "other.txt", "other\n")
	mustGit(t, f.repoDir, "add", ".")
	mustGit(t, f.repoDir, "commit", "-m", "other work")
	mustGit(t, f.repoDir, "checkout", "main")
	otherSHA := mustGit(t, f.repoDir, "rev-parse", "other")
	mb := mustGit(t, f.repoDir, "merge-base", "other", "feature")

	res := decode[SetBaseResult](t, wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": "other"}), http.StatusOK))
	if res.PreviousBase != "main" || res.ApprovalCleared {
		t.Errorf("result flags = %+v", res)
	}
	if res.PR.BaseRef != "other" || res.PR.BaseSHA != otherSHA || res.PR.MergeBaseSHA != mb || res.PR.HeadSHA != pr.HeadSHA {
		t.Errorf("retarget = %+v, want base other sha %s mb %s head %s", res.PR, otherSHA, mb, pr.HeadSHA)
	}
	if res.PR.State != "open" {
		t.Errorf("state = %s, want open", res.PR.State)
	}

	// Same target refreshes in place and does not clear an approval.
	if err := f.store.UpdatePRState(pr.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	res = decode[SetBaseResult](t, wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": " other "}), http.StatusOK))
	if res.ApprovalCleared || res.PR.State != "approved" || res.PR.BaseRef != "other" {
		t.Errorf("same base = %+v", res)
	}

	res = decode[SetBaseResult](t, wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": "main"}), http.StatusOK))
	if !res.ApprovalCleared || res.PR.State != "open" || res.PR.BaseRef != "main" || res.PreviousBase != "other" {
		t.Errorf("approval not cleared: %+v state=%s base=%s", res, res.PR.State, res.PR.BaseRef)
	}

	if err := f.store.UpdatePRState(pr.ID, "changes_requested"); err != nil {
		t.Fatal(err)
	}
	res = decode[SetBaseResult](t, wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": "other"}), http.StatusOK))
	if res.PR.State != "changes_requested" || res.ApprovalCleared || res.PR.BaseRef != "other" {
		t.Errorf("changes_requested retarget = %+v", res)
	}

	for _, base := range []string{"missing", "feature", "", "  "} {
		rec := f.do("POST", "/api/prs/1/base", map[string]string{"base": base})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("base %q status = %d, want 400; body: %s", base, rec.Code, rec.Body.String())
		}
	}
	stored, err := f.store.GetPR(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BaseRef != "other" || stored.State != "changes_requested" {
		t.Errorf("failed retarget mutated PR: %+v", stored)
	}

	if err := f.store.UpdatePRState(pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": "main"}), http.StatusBadRequest)
	if err := f.store.UpdatePRState(pr.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("POST", "/api/prs/1/base", map[string]string{"base": "main"}), http.StatusBadRequest)
	wantStatus(t, f.do("POST", "/api/prs/99/base", map[string]string{"base": "main"}), http.StatusNotFound)
	wantStatus(t, f.do("POST", "/api/prs/1/base", "not-json"), http.StatusBadRequest)
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
	PR         store.PR       `json:"pr"`
	Files      []git.FileDiff `json:"files"`
	FromSHA    string         `json:"from_sha"`
	ToSHA      string         `json:"to_sha"`
	LastReview *store.Review  `json:"last_review"`
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

func TestGetDiffSinceLastReview(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()

	wantStatus(t, f.do("POST", fmt.Sprintf("/api/prs/%d/reviews", pr.Number), map[string]any{
		"verdict": "request_changes", "body": "needs follow-up",
	}), http.StatusCreated)
	oldHead := pr.HeadSHA
	newHead := f.commitOnFeature("c.txt", "more\n")

	resp := decode[diffResp](t, wantStatus(t, f.do("GET", "/api/prs/1/diff?since=last-review", nil), http.StatusOK))
	if resp.FromSHA != oldHead || resp.ToSHA != newHead {
		t.Fatalf("range = %s..%s, want %s..%s", resp.FromSHA, resp.ToSHA, oldHead, newHead)
	}
	if resp.LastReview == nil || resp.LastReview.HeadSHA != oldHead {
		t.Fatalf("last_review = %+v, want head %s", resp.LastReview, oldHead)
	}
	if len(resp.Files) != 1 || resp.Files[0].NewPath != "c.txt" {
		t.Fatalf("files = %+v, want only c.txt", resp.Files)
	}

	full := decode[diffResp](t, wantStatus(t, f.do("GET", "/api/prs/1/diff", nil), http.StatusOK))
	if len(full.Files) != 3 {
		t.Fatalf("full diff files = %+v, want 3 files", full.Files)
	}
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

func TestRefreshMarksOutdatedThreads(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	path := fmt.Sprintf("/api/prs/%d/threads", pr.Number)
	changed := decode[store.Thread](t, wantStatus(t, f.do("POST", path, map[string]any{
		"file_path": "a.txt", "line": 2, "body": "this line looks odd",
	}), http.StatusCreated))
	unchanged := decode[store.Thread](t, wantStatus(t, f.do("POST", path, map[string]any{
		"file_path": "a.txt", "line": 1, "body": "keep this in mind",
	}), http.StatusCreated))
	prLevel := decode[store.Thread](t, wantStatus(t, f.do("POST", path, map[string]any{
		"file_path": "", "line": 0, "body": "overall note",
	}), http.StatusCreated))

	mustGit(t, f.repoDir, "checkout", "feature")
	writeFile(t, f.repoDir, "a.txt", "hello\nchanged after review\nworld\n")
	mustGit(t, f.repoDir, "add", ".")
	mustGit(t, f.repoDir, "commit", "-m", "change reviewed line")
	mustGit(t, f.repoDir, "checkout", "main")

	wantStatus(t, f.do("GET", fmt.Sprintf("/api/prs/%d", pr.Number), nil), http.StatusOK)
	threads := decode[[]store.Thread](t, wantStatus(t, f.do("GET", path, nil), http.StatusOK))
	byID := map[int64]store.Thread{}
	for _, th := range threads {
		byID[th.ID] = th
	}
	if !byID[changed.ID].Outdated {
		t.Errorf("changed thread outdated = false, want true: %+v", byID[changed.ID])
	}
	if byID[unchanged.ID].Outdated {
		t.Errorf("unchanged thread outdated = true, want false: %+v", byID[unchanged.ID])
	}
	if byID[prLevel.ID].Outdated {
		t.Errorf("PR-level thread outdated = true, want false: %+v", byID[prLevel.ID])
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

type mergeResp struct {
	PR             store.PR `json:"pr"`
	NewTip         string   `json:"new_tip"`
	CleanedUp      bool     `json:"cleaned_up"`
	CleanupMessage string   `json:"cleanup_message"`
}

func TestMergePR(t *testing.T) {
	f := newFixture(t)
	f.createPR()

	res := decode[mergeResp](t, wantStatus(t, f.do("POST", "/api/prs/1/merge",
		map[string]any{"cleanup": false}), http.StatusOK))
	if res.PR.State != "merged" {
		t.Errorf("state = %q, want merged", res.PR.State)
	}
	if mainSHA := mustGit(t, f.repoDir, "rev-parse", "main"); mainSHA != res.NewTip {
		t.Errorf("main = %s, want new tip %s", mainSHA, res.NewTip)
	}
	if res.CleanedUp {
		t.Error("cleaned_up = true despite cleanup: false")
	}
	// Feature branch must survive when cleanup was declined.
	if out := mustGit(t, f.repoDir, "branch", "--list", "feature"); out == "" {
		t.Error("feature branch deleted despite cleanup: false")
	}

	// Re-merging is refused.
	wantStatus(t, f.do("POST", "/api/prs/1/merge", nil), http.StatusConflict)
}

func TestMergePRCleanup(t *testing.T) {
	f := newFixture(t)
	// A feature branch in its own worktree, like the real agent flow.
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, f.repoDir, "worktree", "add", "-b", "wt-feature", wt, "main")
	writeFile(t, wt, "d.txt", "wt work\n")
	mustGit(t, wt, "add", ".")
	mustGit(t, wt, "commit", "-m", "wt work")
	pr, err := f.store.CreatePR(store.PR{
		Title: "wt PR", BaseRef: "main", HeadRef: "wt-feature",
		BaseSHA:      mustGit(t, f.repoDir, "rev-parse", "main"),
		MergeBaseSHA: mustGit(t, f.repoDir, "merge-base", "main", "wt-feature"),
		HeadSHA:      mustGit(t, f.repoDir, "rev-parse", "wt-feature"),
		WorktreePath: wt,
	})
	if err != nil {
		t.Fatal(err)
	}

	res := decode[mergeResp](t, wantStatus(t, f.do("POST", fmt.Sprintf("/api/prs/%d/merge", pr.Number),
		map[string]any{"cleanup": true}), http.StatusOK))
	if !res.CleanedUp || res.CleanupMessage != "" {
		t.Errorf("cleanup: %+v", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists: %v", err)
	}
	if out := mustGit(t, f.repoDir, "branch", "--list", "wt-feature"); out != "" {
		t.Errorf("branch still exists: %q", out)
	}
}

func TestMergePRConflict(t *testing.T) {
	f := newFixture(t)
	f.createPR()
	// Conflicting change on main: feature edits a.txt too.
	writeFile(t, f.repoDir, "a.txt", "conflicting\n")
	mustGit(t, f.repoDir, "add", ".")
	mustGit(t, f.repoDir, "commit", "-m", "conflict on main")

	wantStatus(t, f.do("POST", "/api/prs/1/merge", nil), http.StatusConflict)
	stored, err := f.store.GetPRByNumber(1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State == "merged" {
		t.Error("failed merge still set state=merged")
	}
}

// addWorktreePR creates a feature branch in its own worktree with one commit
// and a PR row for it, returning the PR and the worktree path.
func (f *fixture) addWorktreePR(branch string) (*store.PR, string) {
	f.t.Helper()
	wt := filepath.Join(f.t.TempDir(), branch)
	mustGit(f.t, f.repoDir, "worktree", "add", "-b", branch, wt, "main")
	writeFile(f.t, wt, branch+".txt", "work\n")
	mustGit(f.t, wt, "add", ".")
	mustGit(f.t, wt, "commit", "-m", "work on "+branch)
	pr, err := f.store.CreatePR(store.PR{
		Title: branch, BaseRef: "main", HeadRef: branch,
		BaseSHA:      mustGit(f.t, f.repoDir, "rev-parse", "main"),
		MergeBaseSHA: mustGit(f.t, f.repoDir, "merge-base", "main", branch),
		HeadSHA:      mustGit(f.t, f.repoDir, "rev-parse", branch),
		WorktreePath: wt,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return pr, wt
}

func TestMergePRCleanupGuardRepoRoot(t *testing.T) {
	f := newFixture(t)
	pr, wt := f.addWorktreePR("guarded")

	// A server whose git context is rooted inside the PR's own worktree
	// (like a `lgit serve` launched from it) must not remove it.
	repoAtWT, err := git.Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(f.store, repoAtWT).Handler()
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/prs/%d/merge", pr.Number), strings.NewReader(`{"cleanup":true}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := decode[mergeResp](t, wantStatus(t, rec, http.StatusOK))

	if res.PR.State != "merged" {
		t.Errorf("state = %q, want merged", res.PR.State)
	}
	if res.CleanedUp || !strings.Contains(res.CleanupMessage, "cleanup skipped") {
		t.Errorf("guard did not fire: %+v", res)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree was removed despite guard: %v", err)
	}
}

func TestSweep(t *testing.T) {
	f := newFixture(t)
	// PR #1: from the shared fixture — branch "feature" lives in the primary
	// worktree's repo, no separate worktree dir.
	f.createPR()
	// PR #2: real worktree, merged without cleanup.
	pr2, wt := f.addWorktreePR("swept")

	wantStatus(t, f.do("POST", "/api/prs/1/merge", map[string]any{"cleanup": false}), http.StatusOK)
	wantStatus(t, f.do("POST", fmt.Sprintf("/api/prs/%d/merge", pr2.Number),
		map[string]any{"cleanup": false}), http.StatusOK)

	repo, err := git.Open(f.repoDir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := New(f.store, repo).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Err != nil {
			t.Errorf("PR #%d: %v", e.PR.Number, e.Err)
		}
	}
	// Leftovers gone.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists", wt)
	}
	for _, br := range []string{"feature", "swept"} {
		if out := mustGit(t, f.repoDir, "branch", "--list", br); out != "" {
			t.Errorf("branch %s still exists", br)
		}
	}
	// The primary worktree must never be touched.
	if _, err := os.Stat(filepath.Join(f.repoDir, "a.txt")); err != nil {
		t.Errorf("primary worktree damaged: %v", err)
	}

	// Idempotent: nothing left to do.
	entries, err = New(f.store, repo).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("second sweep found %d entries: %+v", len(entries), entries)
	}
}
