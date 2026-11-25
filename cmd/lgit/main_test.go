package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lgit/internal/store"
)

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
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupRepo(t *testing.T) string {
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
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "feature work")
	mustGit(t, dir, "checkout", "main")
	return dir
}

type fixture struct {
	t   *testing.T
	dir string
	st  *store.Store
	pr  *store.PR
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := setupRepo(t)
	t.Chdir(dir)
	if err := run([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.DBPath(filepath.Join(dir, ".git")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pr, err := st.CreatePR(store.PR{
		Title: "test PR", BaseRef: "main", HeadRef: "feature",
		BaseSHA:      mustGit(t, dir, "rev-parse", "main"),
		MergeBaseSHA: mustGit(t, dir, "merge-base", "main", "feature"),
		HeadSHA:      mustGit(t, dir, "rev-parse", "feature"),
		WorktreePath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, st: st, pr: pr}
}

func TestPRComment(t *testing.T) {
	f := newFixture(t)

	if err := run([]string{"pr", "comment", "1", "why brave?", "--file", "a.txt", "--line", "2"}); err != nil {
		t.Fatal(err)
	}
	threads, err := f.st.ListThreads(f.pr.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].FilePath != "a.txt" || threads[0].Line != 2 || threads[0].Side != "new" {
		t.Fatalf("inline = %+v", threads)
	}
	if threads[0].AnchorSHA != f.pr.HeadSHA {
		t.Errorf("anchor = %s, want %s", threads[0].AnchorSHA, f.pr.HeadSHA)
	}
	if len(threads[0].Comments) != 1 || threads[0].Comments[0].Author != "agent" || threads[0].Comments[0].Body != "why brave?" {
		t.Errorf("first comment: %+v", threads[0].Comments)
	}

	if err := run([]string{"pr", "comment", "1", "overall note"}); err != nil {
		t.Fatal(err)
	}
	threads, err = f.st.ListThreads(f.pr.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
	prLevel := threads[1]
	if prLevel.FilePath != "" || prLevel.Line != 0 {
		t.Errorf("PR-level = %+v", prLevel)
	}

	if err := run([]string{"pr", "comment", "1"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("missing body error = %v", err)
	}
	if err := run([]string{"pr", "comment", "1", "x", "--file", "a.txt"}); err == nil || !strings.Contains(err.Error(), "--line is required") {
		t.Errorf("file without line error = %v", err)
	}
	if err := run([]string{"pr", "comment", "1", "x", "--line", "2"}); err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("line without file error = %v", err)
	}
	if err := run([]string{"pr", "comment", "1", "x", "--file", "a.txt", "--line", "2", "--side", "left"}); err == nil || !strings.Contains(err.Error(), "side must be") {
		t.Errorf("bad side error = %v", err)
	}
	if err := run([]string{"pr", "comment", "1", "x", "--author", "bot"}); err == nil || !strings.Contains(err.Error(), "author must be") {
		t.Errorf("bad author error = %v", err)
	}
	if err := run([]string{"pr", "comment", "99", "x"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing PR error = %v", err)
	}
}

func TestPRBase(t *testing.T) {
	f := newFixture(t)
	mustGit(t, f.dir, "checkout", "-b", "other")
	writeFile(t, f.dir, "other.txt", "other\n")
	mustGit(t, f.dir, "add", ".")
	mustGit(t, f.dir, "commit", "-m", "other work")
	mustGit(t, f.dir, "checkout", "main")

	if err := run([]string{"pr", "base", "1", "other"}); err != nil {
		t.Fatal(err)
	}
	pr, err := f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.BaseRef != "other" {
		t.Fatalf("base = %q, want other", pr.BaseRef)
	}
	if pr.BaseSHA != mustGit(t, f.dir, "rev-parse", "other") {
		t.Errorf("base sha = %s", pr.BaseSHA)
	}
	if pr.MergeBaseSHA != mustGit(t, f.dir, "merge-base", "other", "feature") {
		t.Errorf("merge-base = %s", pr.MergeBaseSHA)
	}

	if err := run([]string{"pr", "base", "1", "other"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pr", "base", "1", "--base", "main"}); err != nil {
		t.Fatal(err)
	}
	pr, err = f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.BaseRef != "main" {
		t.Errorf("flag form base = %q, want main", pr.BaseRef)
	}

	if err := f.st.UpdatePRState(f.pr.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pr", "base", "1", "other"}); err != nil {
		t.Fatal(err)
	}
	pr, err = f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "open" || pr.BaseRef != "other" {
		t.Errorf("approved retarget = state %q base %q", pr.State, pr.BaseRef)
	}

	if err := run([]string{"pr", "base"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("missing args error = %v", err)
	}
	if err := run([]string{"pr", "base", "1", "other", "--base", "main"}); err == nil || !strings.Contains(err.Error(), "once") {
		t.Errorf("both forms error = %v", err)
	}
	if err := run([]string{"pr", "base", "1", "nope"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing branch error = %v", err)
	}
	if err := run([]string{"pr", "base", "1", "feature"}); err == nil || !strings.Contains(err.Error(), "PR head") {
		t.Errorf("head as base error = %v", err)
	}
	if err := run([]string{"pr", "base", "99", "main"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing PR error = %v", err)
	}

	if err := f.st.UpdatePRState(f.pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pr", "base", "1", "main"}); err == nil || !strings.Contains(err.Error(), "merged") {
		t.Errorf("merged error = %v", err)
	}
}

func TestPRReview(t *testing.T) {
	f := newFixture(t)

	if err := run([]string{"pr", "review", "1", "--request-changes", "--body", "needs work"}); err != nil {
		t.Fatal(err)
	}
	pr, err := f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "changes_requested" {
		t.Errorf("state = %q, want changes_requested", pr.State)
	}
	rev, err := f.st.LastReview(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Verdict != "request_changes" || rev.Body != "needs work" {
		t.Errorf("review = %+v", rev)
	}

	if err := run([]string{"pr", "review", "1", "--approve"}); err != nil {
		t.Fatal(err)
	}
	pr, err = f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "approved" {
		t.Errorf("state = %q, want approved", pr.State)
	}

	if err := run([]string{"pr", "review", "1"}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("missing verdict error = %v", err)
	}
	if err := run([]string{"pr", "review", "1", "--approve", "--request-changes"}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("both verdicts error = %v", err)
	}
	if err := run([]string{"pr", "review", "99", "--approve"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing PR error = %v", err)
	}

	if err := f.st.UpdatePRState(f.pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pr", "review", "1", "--approve"}); err != nil {
		t.Fatal(err)
	}
	pr, err = f.st.GetPR(f.pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "merged" {
		t.Errorf("approve on merged PR set state to %q, want merged", pr.State)
	}
}
