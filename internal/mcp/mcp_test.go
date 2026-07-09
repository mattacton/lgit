package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupRepo creates a scratch repo with main plus a feature branch one commit
// ahead (main left checked out). No lgit DB is created.
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

// connect wires an in-process MCP client to a lgit MCP server rooted at dir.
func connect(t *testing.T, dir string) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := sdk.NewInMemoryTransports()
	if _, err := NewServer(dir).Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "lgit-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// fixture is a scratch repo with an initialized lgit store and a connected
// MCP client session.
type fixture struct {
	t   *testing.T
	dir string
	st  *store.Store
	cs  *sdk.ClientSession
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := setupRepo(t)
	st, err := store.Open(store.DBPath(filepath.Join(dir, ".git")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fixture{t: t, dir: dir, st: st, cs: connect(t, dir)}
}

// createPR snapshots main..feature into a PR row, like `lgit pr create`.
func (f *fixture) createPR() *store.PR {
	f.t.Helper()
	pr, err := f.st.CreatePR(store.PR{
		Title: "test PR", BaseRef: "main", HeadRef: "feature",
		BaseSHA:      mustGit(f.t, f.dir, "rev-parse", "main"),
		MergeBaseSHA: mustGit(f.t, f.dir, "merge-base", "main", "feature"),
		HeadSHA:      mustGit(f.t, f.dir, "rev-parse", "feature"),
		WorktreePath: f.dir,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return pr
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func textOf(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// callOK invokes a tool, requires success, and decodes the structured output.
func callOK[T any](t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res := call(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s returned tool error: %s", name, textOf(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decoding %s output %s: %v", name, b, err)
	}
	return v
}

// callErr invokes a tool, requires a tool error, and returns its message.
func callErr(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res := call(t, cs, name, args)
	if !res.IsError {
		t.Fatalf("%s succeeded, want tool error", name)
	}
	return textOf(res)
}

func TestToolsRegistered(t *testing.T) {
	f := newFixture(t)
	res, err := f.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"get_pr", "get_unresolved_comments", "list_prs",
		"mark_pr_ready", "reply_comment", "resolve_comment"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestListPRs(t *testing.T) {
	f := newFixture(t)

	if got := callOK[ListPRsResult](t, f.cs, "list_prs", nil); len(got.PRs) != 0 {
		t.Errorf("empty store: %+v", got.PRs)
	}

	f.createPR()
	pr2 := f.createPR()
	if err := f.st.UpdatePRState(pr2.ID, "approved"); err != nil {
		t.Fatal(err)
	}

	got := callOK[ListPRsResult](t, f.cs, "list_prs", nil)
	if len(got.PRs) != 2 || got.PRs[0].Number != 2 || got.PRs[1].Number != 1 {
		t.Errorf("list = %+v, want numbers [2 1]", got.PRs)
	}

	got = callOK[ListPRsResult](t, f.cs, "list_prs", map[string]any{"state": "approved"})
	if len(got.PRs) != 1 || got.PRs[0].Number != 2 {
		t.Errorf("state filter = %+v, want just #2", got.PRs)
	}

	if msg := callErr(t, f.cs, "list_prs", map[string]any{"state": "bogus"}); !strings.Contains(msg, "unknown state") {
		t.Errorf("bad state error = %q", msg)
	}
}

func TestGetPR(t *testing.T) {
	f := newFixture(t)
	created := f.createPR()

	got := callOK[store.PR](t, f.cs, "get_pr", map[string]any{"number": 1})
	if got.Number != created.Number || got.Title != created.Title || got.HeadSHA != created.HeadSHA {
		t.Errorf("got %+v, want %+v", got, created)
	}

	// New commits on the head branch are picked up (range refresh).
	mustGit(t, f.dir, "checkout", "feature")
	writeFile(t, f.dir, "c.txt", "more\n")
	mustGit(t, f.dir, "add", ".")
	mustGit(t, f.dir, "commit", "-m", "more work")
	newHead := mustGit(t, f.dir, "rev-parse", "feature")
	mustGit(t, f.dir, "checkout", "main")

	got = callOK[store.PR](t, f.cs, "get_pr", map[string]any{"number": 1})
	if got.HeadSHA != newHead {
		t.Errorf("head_sha = %s, want refreshed %s", got.HeadSHA, newHead)
	}

	if msg := callErr(t, f.cs, "get_pr", map[string]any{"number": 99}); !strings.Contains(msg, "not found") {
		t.Errorf("missing PR error = %q", msg)
	}
}

func TestCommentLoop(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	th, err := f.st.CreateThread(store.Thread{
		PRID: pr.ID, FilePath: "a.txt", Line: 2, Side: "new", AnchorSHA: pr.HeadSHA,
	}, "user", "please fix")
	if err != nil {
		t.Fatal(err)
	}

	got := callOK[ThreadsResult](t, f.cs, "get_unresolved_comments", map[string]any{"number": 1})
	if len(got.Threads) != 1 || got.Threads[0].FilePath != "a.txt" || len(got.Threads[0].Comments) != 1 {
		t.Fatalf("unresolved = %+v", got.Threads)
	}

	c := callOK[store.Comment](t, f.cs, "reply_comment", map[string]any{
		"thread_id": th.ID, "body": "fixed in abc123",
	})
	if c.Author != "agent" || c.Body != "fixed in abc123" {
		t.Errorf("reply = %+v", c)
	}

	resolved := callOK[store.Thread](t, f.cs, "resolve_comment", map[string]any{"thread_id": th.ID})
	if !resolved.Resolved {
		t.Error("thread not resolved")
	}
	if len(resolved.Comments) != 2 {
		t.Errorf("thread has %d comments, want 2", len(resolved.Comments))
	}

	got = callOK[ThreadsResult](t, f.cs, "get_unresolved_comments", map[string]any{"number": 1})
	if len(got.Threads) != 0 {
		t.Errorf("after resolve, unresolved = %+v", got.Threads)
	}

	if msg := callErr(t, f.cs, "reply_comment", map[string]any{"thread_id": th.ID, "body": "  "}); !strings.Contains(msg, "body is required") {
		t.Errorf("empty body error = %q", msg)
	}
	if msg := callErr(t, f.cs, "reply_comment", map[string]any{"thread_id": 999, "body": "x"}); !strings.Contains(msg, "not found") {
		t.Errorf("missing thread error = %q", msg)
	}
	if msg := callErr(t, f.cs, "resolve_comment", map[string]any{"thread_id": 999}); !strings.Contains(msg, "not found") {
		t.Errorf("missing thread error = %q", msg)
	}
}

func TestMarkPRReady(t *testing.T) {
	f := newFixture(t)
	pr := f.createPR()
	if err := f.st.UpdatePRState(pr.ID, "changes_requested"); err != nil {
		t.Fatal(err)
	}

	got := callOK[store.PR](t, f.cs, "mark_pr_ready", map[string]any{"number": 1})
	if got.State != "open" {
		t.Errorf("state = %q, want open", got.State)
	}

	// Idempotent on an already-open PR.
	got = callOK[store.PR](t, f.cs, "mark_pr_ready", map[string]any{"number": 1})
	if got.State != "open" {
		t.Errorf("second call state = %q, want open", got.State)
	}

	if err := f.st.UpdatePRState(pr.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	if msg := callErr(t, f.cs, "mark_pr_ready", map[string]any{"number": 1}); !strings.Contains(msg, "merged") {
		t.Errorf("merged PR error = %q", msg)
	}
}

func TestFailSoftOutsideGitRepo(t *testing.T) {
	cs := connect(t, t.TempDir()) // server startup must succeed anywhere
	msg := callErr(t, cs, "list_prs", nil)
	if !strings.Contains(msg, "not inside a git repository") {
		t.Errorf("error = %q, want mention of git repository", msg)
	}
}

func TestFailSoftUninitializedRepo(t *testing.T) {
	cs := connect(t, setupRepo(t)) // git repo, but no `lgit init`
	msg := callErr(t, cs, "list_prs", nil)
	if !strings.Contains(msg, "lgit init") {
		t.Errorf("error = %q, want mention of lgit init", msg)
	}
}
