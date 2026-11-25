package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "lgit", "lgit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func samplePR() PR {
	return PR{
		Title: "Add feature", Body: "desc",
		BaseRef: "main", HeadRef: "feature",
		BaseSHA: "aaa", MergeBaseSHA: "bbb", HeadSHA: "ccc",
		WorktreePath: "/tmp/wt",
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lgit", "lgit.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreatePR(samplePR()); err != nil {
		t.Fatal(err)
	}
	s1.Close()
	// Re-open over existing DB: schema apply must be a no-op, data preserved.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	prs, err := s2.ListPRs("")
	if err != nil || len(prs) != 1 {
		t.Fatalf("prs=%v err=%v", prs, err)
	}
}

func TestPRCRUD(t *testing.T) {
	s := openTest(t)
	p1, err := s.CreatePR(samplePR())
	if err != nil {
		t.Fatal(err)
	}
	if p1.Number != 1 || p1.State != "open" || p1.ID == 0 {
		t.Errorf("p1 = %+v", p1)
	}
	p2, err := s.CreatePR(samplePR())
	if err != nil {
		t.Fatal(err)
	}
	if p2.Number != 2 {
		t.Errorf("number = %d, want 2", p2.Number)
	}

	got, err := s.GetPRByNumber(1)
	if err != nil || got.ID != p1.ID {
		t.Fatalf("GetPRByNumber: %v %v", got, err)
	}
	if got.Title != "Add feature" || got.BaseRef != "main" || got.HeadSHA != "ccc" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if err := s.UpdatePRState(p1.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPR(p1.ID)
	if got.State != "approved" {
		t.Errorf("state = %s", got.State)
	}

	if err := s.UpdatePRRange(p1.ID, "a2", "b2", "c2"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPR(p1.ID)
	if got.BaseSHA != "a2" || got.MergeBaseSHA != "b2" || got.HeadSHA != "c2" {
		t.Errorf("range update: %+v", got)
	}

	if err := s.UpdatePRBase(p1.ID, "develop", "d1", "m1", "h1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPR(p1.ID)
	if got.BaseRef != "develop" || got.BaseSHA != "d1" || got.MergeBaseSHA != "m1" || got.HeadSHA != "h1" {
		t.Errorf("base update: %+v", got)
	}
	if err := s.UpdatePRBase(999, "develop", "d", "m", "h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	open, err := s.ListPRs("open")
	if err != nil || len(open) != 1 || open[0].ID != p2.ID {
		t.Errorf("ListPRs(open) = %+v, %v", open, err)
	}
	all, _ := s.ListPRs("")
	if len(all) != 2 || all[0].Number != 2 {
		t.Errorf("ListPRs ordering: %+v", all)
	}

	if _, err := s.GetPR(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.UpdatePRState(999, "open"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInvalidState(t *testing.T) {
	s := openTest(t)
	p, _ := s.CreatePR(samplePR())
	if err := s.UpdatePRState(p.ID, "bogus"); err == nil {
		t.Error("expected CHECK constraint failure")
	}
}

func TestThreadsAndComments(t *testing.T) {
	s := openTest(t)
	p, _ := s.CreatePR(samplePR())

	th, err := s.CreateThread(Thread{
		PRID: p.ID, FilePath: "a.txt", Line: 3, Side: "new", AnchorSHA: "ccc",
	}, "user", "this looks wrong")
	if err != nil {
		t.Fatal(err)
	}
	if th.Resolved || len(th.Comments) != 1 || th.Comments[0].Body != "this looks wrong" {
		t.Errorf("thread = %+v", th)
	}

	if _, err := s.AddComment(th.ID, "agent", "fixed in abc123"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetThread(th.ID)
	if len(got.Comments) != 2 || got.Comments[1].Author != "agent" {
		t.Errorf("comments = %+v", got.Comments)
	}

	// PR-level thread (no file).
	if _, err := s.CreateThread(Thread{PRID: p.ID, Side: "new"}, "user", "overall: nice"); err != nil {
		t.Fatal(err)
	}

	threads, err := s.ListThreads(p.ID, false)
	if err != nil || len(threads) != 2 {
		t.Fatalf("threads = %+v, %v", threads, err)
	}

	if err := s.SetThreadResolved(th.ID, true); err != nil {
		t.Fatal(err)
	}
	unresolved, _ := s.ListThreads(p.ID, true)
	if len(unresolved) != 1 || unresolved[0].FilePath != "" {
		t.Errorf("unresolved = %+v", unresolved)
	}

	if _, err := s.AddComment(999, "user", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.SetThreadResolved(999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestReviews(t *testing.T) {
	s := openTest(t)
	p, _ := s.CreatePR(samplePR())
	r, err := s.CreateReview(p.ID, "request_changes", "needs work", p.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 || r.Verdict != "request_changes" || r.HeadSHA != p.HeadSHA {
		t.Errorf("review = %+v", r)
	}
	last, err := s.LastReview(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last.ID != r.ID || last.HeadSHA != p.HeadSHA {
		t.Errorf("last review = %+v, want %+v", last, r)
	}
	if _, err := s.CreateReview(p.ID, "bogus", "", p.HeadSHA); err == nil {
		t.Error("expected CHECK constraint failure for bad verdict")
	}
	if _, err := s.LastReview(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing last review: err = %v", err)
	}
}

func TestMarkPRReady(t *testing.T) {
	s := openTest(t)
	pr, err := s.CreatePR(samplePR())
	if err != nil {
		t.Fatal(err)
	}

	// Idempotent on an already-open PR.
	got, err := s.MarkPRReady(pr.ID)
	if err != nil || got.State != "open" {
		t.Fatalf("open PR: got %+v, err %v", got, err)
	}

	// changes_requested flips back to open.
	if err := s.UpdatePRState(pr.ID, "changes_requested"); err != nil {
		t.Fatal(err)
	}
	got, err = s.MarkPRReady(pr.ID)
	if err != nil || got.State != "open" {
		t.Fatalf("changes_requested PR: got %+v, err %v", got, err)
	}

	// Any other state is refused, and left unchanged.
	for _, state := range []string{"approved", "merged", "closed"} {
		if err := s.UpdatePRState(pr.ID, state); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkPRReady(pr.ID); err == nil {
			t.Errorf("state %s: expected error", state)
		}
		if cur, _ := s.GetPR(pr.ID); cur.State != state {
			t.Errorf("state %s was changed to %s", state, cur.State)
		}
	}

	if _, err := s.MarkPRReady(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing PR: err = %v", err)
	}
}
