// Package mcp exposes lgit's agent-facing operations (docs/SPEC.md §3) as MCP
// tools. The repo and store are resolved per tool call, not at startup, so the
// server starts cleanly anywhere (e.g. registered user-wide in an MCP client)
// and tools report a useful error when called outside an lgit-initialized repo.
package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"lgit/internal/git"
	"lgit/internal/server"
	"lgit/internal/store"
)

// Serve runs the lgit MCP server over stdio until the client disconnects.
// dir is the directory the repo/store are resolved from.
func Serve(ctx context.Context, dir string) error {
	return NewServer(dir).Run(ctx, &sdk.StdioTransport{})
}

// NewServer returns an MCP server exposing lgit's tools, resolving the
// repo/store from dir (typically the process working directory).
func NewServer(dir string) *sdk.Server {
	t := &tools{dir: dir}
	srv := sdk.NewServer(&sdk.Implementation{Name: "lgit", Version: "0.1.0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "list_prs",
		Description: "List lgit PRs (local worktree code review), optionally filtered by state."},
		t.listPRs)
	sdk.AddTool(srv, &sdk.Tool{Name: "get_pr",
		Description: "Get one lgit PR by number, with its diff range refreshed to the branch's current head."},
		t.getPR)
	sdk.AddTool(srv, &sdk.Tool{Name: "get_unresolved_comments",
		Description: "List a PR's unresolved review threads (file, line, comments). Address each one, then reply_comment and resolve_comment."},
		t.getUnresolvedComments)
	sdk.AddTool(srv, &sdk.Tool{Name: "reply_comment",
		Description: "Reply to a review thread as the agent — say what you changed and where."},
		t.replyComment)
	sdk.AddTool(srv, &sdk.Tool{Name: "resolve_comment",
		Description: "Mark a review thread resolved. Only resolve threads you actually addressed."},
		t.resolveComment)
	sdk.AddTool(srv, &sdk.Tool{Name: "mark_pr_ready",
		Description: "After addressing all review feedback, flip a changes_requested PR back to open to signal it is ready for re-review."},
		t.markPRReady)
	return srv
}

// tools carries the directory tool calls resolve the repo/store from.
type tools struct {
	dir string
}

// open resolves the git repo and lgit store for one tool call; the caller
// closes the store. Like the CLI, it requires `lgit init` to have been run.
func (t *tools) open() (*git.Repo, *store.Store, error) {
	repo, err := git.Open(t.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not inside a git repository; run the lgit MCP server from within an lgit repo", t.dir)
	}
	common, err := repo.CommonDir()
	if err != nil {
		return nil, nil, err
	}
	path := store.DBPath(common)
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("lgit DB not found at %s — run `lgit init` first", path)
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return repo, st, nil
}

func prByNumber(st *store.Store, number int64) (*store.PR, error) {
	pr, err := st.GetPRByNumber(number)
	if err != nil {
		return nil, fmt.Errorf("PR #%d: %w", number, err)
	}
	return pr, nil
}

// ListPRsArgs is the input for list_prs.
type ListPRsArgs struct {
	State string `json:"state,omitempty" jsonschema:"optional state filter: open, changes_requested, approved, merged, or closed"`
}

// ListPRsResult is the output of list_prs.
type ListPRsResult struct {
	PRs []store.PR `json:"prs"`
}

func (t *tools) listPRs(ctx context.Context, req *sdk.CallToolRequest, args ListPRsArgs) (*sdk.CallToolResult, ListPRsResult, error) {
	var zero ListPRsResult
	switch args.State {
	case "", "open", "changes_requested", "approved", "merged", "closed":
	default:
		return nil, zero, fmt.Errorf("unknown state %q: want open, changes_requested, approved, merged, or closed", args.State)
	}
	_, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	prs, err := st.ListPRs(args.State)
	if err != nil {
		return nil, zero, err
	}
	if prs == nil {
		prs = []store.PR{}
	}
	return nil, ListPRsResult{PRs: prs}, nil
}

// PRArgs identifies a PR by its user-facing number.
type PRArgs struct {
	Number int64 `json:"number" jsonschema:"the PR number"`
}

func (t *tools) getPR(ctx context.Context, req *sdk.CallToolRequest, args PRArgs) (*sdk.CallToolResult, store.PR, error) {
	var zero store.PR
	repo, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	pr, err := prByNumber(st, args.Number)
	if err != nil {
		return nil, zero, err
	}
	pr = server.New(st, repo).RefreshPR(pr)
	return nil, *pr, nil
}

// ThreadsResult is the output of get_unresolved_comments.
type ThreadsResult struct {
	Threads []store.Thread `json:"threads"`
}

func (t *tools) getUnresolvedComments(ctx context.Context, req *sdk.CallToolRequest, args PRArgs) (*sdk.CallToolResult, ThreadsResult, error) {
	var zero ThreadsResult
	_, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	pr, err := prByNumber(st, args.Number)
	if err != nil {
		return nil, zero, err
	}
	threads, err := st.ListThreads(pr.ID, true)
	if err != nil {
		return nil, zero, err
	}
	return nil, ThreadsResult{Threads: threads}, nil
}

// ReplyArgs is the input for reply_comment.
type ReplyArgs struct {
	ThreadID int64  `json:"thread_id" jsonschema:"the review thread id"`
	Body     string `json:"body" jsonschema:"the reply text, e.g. what was changed and in which commit"`
}

func (t *tools) replyComment(ctx context.Context, req *sdk.CallToolRequest, args ReplyArgs) (*sdk.CallToolResult, store.Comment, error) {
	var zero store.Comment
	if strings.TrimSpace(args.Body) == "" {
		return nil, zero, fmt.Errorf("body is required")
	}
	_, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	// MCP is the agent-facing surface, so the author is always "agent".
	c, err := st.AddComment(args.ThreadID, "agent", args.Body)
	if err != nil {
		return nil, zero, fmt.Errorf("thread %d: %w", args.ThreadID, err)
	}
	return nil, *c, nil
}

// ResolveArgs is the input for resolve_comment.
type ResolveArgs struct {
	ThreadID int64 `json:"thread_id" jsonschema:"the review thread id"`
}

func (t *tools) resolveComment(ctx context.Context, req *sdk.CallToolRequest, args ResolveArgs) (*sdk.CallToolResult, store.Thread, error) {
	var zero store.Thread
	_, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	if err := st.SetThreadResolved(args.ThreadID, true); err != nil {
		return nil, zero, fmt.Errorf("thread %d: %w", args.ThreadID, err)
	}
	th, err := st.GetThread(args.ThreadID)
	if err != nil {
		return nil, zero, err
	}
	return nil, *th, nil
}

func (t *tools) markPRReady(ctx context.Context, req *sdk.CallToolRequest, args PRArgs) (*sdk.CallToolResult, store.PR, error) {
	var zero store.PR
	_, st, err := t.open()
	if err != nil {
		return nil, zero, err
	}
	defer st.Close()
	pr, err := prByNumber(st, args.Number)
	if err != nil {
		return nil, zero, err
	}
	switch pr.State {
	case "open":
		// Already awaiting review; idempotent no-op.
		return nil, *pr, nil
	case "changes_requested":
		if err := st.UpdatePRState(pr.ID, "open"); err != nil {
			return nil, zero, err
		}
		fresh, err := st.GetPR(pr.ID)
		if err != nil {
			return nil, zero, err
		}
		return nil, *fresh, nil
	default:
		return nil, zero, fmt.Errorf("PR #%d is %s; only open or changes_requested PRs can be marked ready", pr.Number, pr.State)
	}
}
