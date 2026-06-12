package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mustGit runs git in dir, failing the test on error.
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

// setupRepo creates a temp repo with main (one commit) and a feature branch
// in a separate worktree with one extra commit. Returns repo dir and worktree dir.
func setupRepo(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-b", "main")
	mustGit(t, repo, "config", "user.name", "test")
	mustGit(t, repo, "config", "user.email", "test@test")
	writeFile(t, repo, "a.txt", "hello\nworld\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-m", "initial")

	wt := filepath.Join(base, "wt-feature")
	mustGit(t, repo, "worktree", "add", "-b", "feature", wt, "main")
	writeFile(t, wt, "a.txt", "hello\nbrave new\nworld\n")
	writeFile(t, wt, "b.go", "package main\n")
	mustGit(t, wt, "add", ".")
	mustGit(t, wt, "commit", "-m", "feature work")
	return repo, wt
}

func TestOpenAndBasics(t *testing.T) {
	repo, wt := setupRepo(t)

	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := r.CurrentBranch()
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature" {
		t.Errorf("branch = %q, want feature", branch)
	}
	top, err := r.Toplevel()
	if err != nil {
		t.Fatal(err)
	}
	if eval, _ := filepath.EvalSymlinks(wt); filepath.Clean(top) != eval && filepath.Clean(top) != wt {
		t.Errorf("toplevel = %q, want %q", top, wt)
	}
	common, err := r.CommonDir()
	if err != nil {
		t.Fatal(err)
	}
	wantCommon := filepath.Join(repo, ".git")
	if eval, _ := filepath.EvalSymlinks(wantCommon); filepath.Clean(common) != wantCommon && filepath.Clean(common) != eval {
		t.Errorf("common dir = %q, want %q", common, wantCommon)
	}
}

func TestMergeBaseAndDiff(t *testing.T) {
	repo, wt := setupRepo(t)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}

	mainSHA := mustGit(t, repo, "rev-parse", "main")
	mb, err := r.MergeBase("main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if mb != mainSHA {
		t.Errorf("merge-base = %s, want %s", mb, mainSHA)
	}

	headSHA, err := r.RevParse("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.Diff(mb, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(files), files)
	}
	byPath := map[string]FileDiff{}
	for _, f := range files {
		byPath[f.NewPath] = f
	}
	a := byPath["a.txt"]
	if a.Status != "modified" || a.Additions != 1 || a.Deletions != 0 {
		t.Errorf("a.txt: %+v", a)
	}
	if !strings.Contains(a.Patch, "+brave new") {
		t.Errorf("a.txt patch missing change:\n%s", a.Patch)
	}
	b := byPath["b.go"]
	if b.Status != "added" || b.OldPath != "" || b.Additions != 1 {
		t.Errorf("b.go: %+v", b)
	}

	// Diff range is recomputed: a new commit on feature must appear.
	writeFile(t, wt, "c.txt", "more\n")
	mustGit(t, wt, "add", ".")
	mustGit(t, wt, "commit", "-m", "more work")
	newHead, _ := r.RevParse("HEAD")
	files, err = r.Diff(mb, newHead)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("after new commit got %d files, want 3", len(files))
	}
}

func TestWorktrees(t *testing.T) {
	repo, wt := setupRepo(t)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	wts, err := r.Worktrees()
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 {
		t.Fatalf("got %d worktrees, want 2", len(wts))
	}
	mainWT, err := r.WorktreeForBranch("main")
	if err != nil {
		t.Fatal(err)
	}
	if mainWT == nil {
		t.Fatal("main worktree not found")
	}
	if eval, _ := filepath.EvalSymlinks(repo); filepath.Clean(mainWT.Path) != repo && filepath.Clean(mainWT.Path) != eval {
		t.Errorf("main worktree path = %q, want %q", mainWT.Path, repo)
	}
	featWT, err := r.WorktreeForBranch("feature")
	if err != nil || featWT == nil {
		t.Fatalf("feature worktree: %v %v", featWT, err)
	}
	none, err := r.WorktreeForBranch("nope")
	if err != nil || none != nil {
		t.Errorf("expected nil for unknown branch, got %v", none)
	}
}

// diverge makes main move ahead so rebase/merge actually has work to do.
func diverge(t *testing.T, repo string) {
	writeFile(t, repo, "main-only.txt", "main change\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-m", "main moves ahead")
}

func TestMergeRebase(t *testing.T) {
	repo, wt := setupRepo(t)
	diverge(t, repo)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}

	newTip, err := r.Merge("main", "feature", MergeRebase, "msg")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, repo, "rev-parse", "main"); got != newTip {
		t.Errorf("main = %s, want %s", got, newTip)
	}
	// Linear history: rebased commit's parent chain contains main's old tip.
	log := mustGit(t, repo, "log", "--oneline", "main")
	if lines := strings.Count(log, "\n") + 1; lines != 3 {
		t.Errorf("expected 3 commits on main, got %d:\n%s", lines, log)
	}
	if mustGit(t, repo, "rev-list", "--merges", "main") != "" {
		t.Error("rebase merge should produce no merge commits")
	}
	// Working tree of repo (main checked out) must reflect the merge.
	if _, err := os.Stat(filepath.Join(repo, "b.go")); err != nil {
		t.Errorf("b.go missing from main worktree after merge: %v", err)
	}
	// Main worktree must be clean (not disturbed).
	if out := mustGit(t, repo, "status", "--porcelain"); out != "" {
		t.Errorf("main worktree dirty after merge:\n%s", out)
	}
	// Feature worktree untouched.
	if out := mustGit(t, wt, "status", "--porcelain"); out != "" {
		t.Errorf("feature worktree dirty after merge:\n%s", out)
	}
}

func TestMergeSquash(t *testing.T) {
	repo, wt := setupRepo(t)
	diverge(t, repo)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	newTip, err := r.Merge("main", "feature", MergeSquash, "squashed feature")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, repo, "rev-parse", "main"); got != newTip {
		t.Errorf("main = %s, want %s", got, newTip)
	}
	msg := mustGit(t, repo, "log", "-1", "--format=%s", "main")
	if msg != "squashed feature" {
		t.Errorf("squash commit msg = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.go")); err != nil {
		t.Errorf("b.go missing after squash merge: %v", err)
	}
}

func TestMergeMergeCommit(t *testing.T) {
	repo, wt := setupRepo(t)
	diverge(t, repo)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	newTip, err := r.Merge("main", "feature", MergeMerge, "Merge PR #1")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, repo, "rev-parse", "main"); got != newTip {
		t.Errorf("main = %s, want %s", got, newTip)
	}
	if mustGit(t, repo, "rev-list", "--merges", "main") == "" {
		t.Error("expected a merge commit on main")
	}
}

func TestMergeRefusesDirtyBaseWorktree(t *testing.T) {
	repo, wt := setupRepo(t)
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "a.txt", "dirty\n")
	if _, err := r.Merge("main", "feature", MergeRebase, "msg"); err == nil {
		t.Fatal("expected error merging into dirty base worktree")
	}
}

func TestMergeBaseNotCheckedOut(t *testing.T) {
	repo, wt := setupRepo(t)
	// Detach the main worktree so `main` is not checked out anywhere.
	mustGit(t, repo, "checkout", "--detach")
	r, err := Open(wt)
	if err != nil {
		t.Fatal(err)
	}
	newTip, err := r.Merge("main", "feature", MergeRebase, "msg")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, repo, "rev-parse", "main"); got != newTip {
		t.Errorf("main = %s, want %s", got, newTip)
	}
}

func TestCleanup(t *testing.T) {
	repo, wt := setupRepo(t)
	r, err := Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Merge("main", "feature", MergeRebase, "msg"); err != nil {
		t.Fatal(err)
	}
	if err := r.Cleanup(wt, "feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists")
	}
	if out := mustGit(t, repo, "branch", "--list", "feature"); out != "" {
		t.Errorf("feature branch still exists: %q", out)
	}
}

func TestParseDiffRename(t *testing.T) {
	raw := "diff --git a/old.txt b/new.txt\n" +
		"similarity index 100%\n" +
		"rename from old.txt\n" +
		"rename to new.txt\n"
	files := parseDiff(raw)
	if len(files) != 1 {
		t.Fatalf("got %d files", len(files))
	}
	f := files[0]
	if f.Status != "renamed" || f.OldPath != "old.txt" || f.NewPath != "new.txt" {
		t.Errorf("rename parse: %+v", f)
	}
}
