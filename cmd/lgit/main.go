// Command lgit is a local-only PR review tool for git-worktree agent
// workflows. See docs/SPEC.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"lgit/internal/git"
	"lgit/internal/mcp"
	"lgit/internal/server"
	"lgit/internal/store"
)

const usage = `lgit — local-only PR review for git worktrees

Usage:
  lgit init                                      create the lgit DB at the shared git dir
  lgit pr create [--title T] [--base main]       create a PR from the current worktree
  lgit pr list [--state S] [--json]              list PRs
  lgit pr view <number>                          show one PR
  lgit pr comments <number> [--unresolved] [--json]
                                                 list comment threads (JSON = agent contract)
  lgit pr reply <thread-id> <body> [--author agent]
                                                 reply to a thread
  lgit pr resolve <thread-id>                    resolve a thread
  lgit pr ready <number>                         mark a changes_requested PR ready for re-review
  lgit serve [--port 7333]                       start the web review UI + JSON API
  lgit mcp                                       serve MCP tools over stdio (agent integration)
  lgit merge <number> [--rebase|--squash|--merge] [--cleanup]
                                                 integrate the PR (default: rebase)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lgit:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "init":
		return cmdInit()
	case "pr":
		if len(args) < 2 {
			return fmt.Errorf("usage: lgit pr <create|list|view|comments|reply|resolve|ready> ...")
		}
		switch args[1] {
		case "create":
			return cmdPRCreate(args[2:])
		case "list":
			return cmdPRList(args[2:])
		case "view":
			return cmdPRView(args[2:])
		case "comments":
			return cmdPRComments(args[2:])
		case "reply":
			return cmdPRReply(args[2:])
		case "resolve":
			return cmdPRResolve(args[2:])
		case "ready":
			return cmdPRReady(args[2:])
		default:
			return fmt.Errorf("unknown pr subcommand %q", args[1])
		}
	case "serve":
		return cmdServe(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "merge":
		return cmdMerge(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (run `lgit help`)", args[0])
	}
}

// openRepo opens the git repo at the current directory.
func openRepo() (*git.Repo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return git.Open(cwd)
}

// openStore opens the shared lgit DB for the current repo, requiring that
// `lgit init` has been run.
func openStore(repo *git.Repo) (*store.Store, error) {
	common, err := repo.CommonDir()
	if err != nil {
		return nil, err
	}
	path := store.DBPath(common)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("lgit DB not found at %s — run `lgit init` first", path)
	}
	return store.Open(path)
}

func cmdInit() error {
	repo, err := openRepo()
	if err != nil {
		return err
	}
	common, err := repo.CommonDir()
	if err != nil {
		return err
	}
	path := store.DBPath(common)
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	fmt.Printf("initialized lgit DB at %s\n", path)
	return nil
}

func cmdPRCreate(args []string) error {
	fs := flag.NewFlagSet("pr create", flag.ContinueOnError)
	title := fs.String("title", "", "PR title (defaults to head branch name)")
	body := fs.String("body", "", "PR description")
	base := fs.String("base", "main", "base branch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := openRepo()
	if err != nil {
		return err
	}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()

	head, err := repo.CurrentBranch()
	if err != nil {
		return err
	}
	if head == *base {
		return fmt.Errorf("current branch %q is the base branch; switch to a feature worktree", head)
	}
	baseSHA, err := repo.RevParse(*base)
	if err != nil {
		return fmt.Errorf("resolving base %q: %w", *base, err)
	}
	headSHA, err := repo.RevParse("HEAD")
	if err != nil {
		return err
	}
	mergeBase, err := repo.MergeBase(*base, head)
	if err != nil {
		return err
	}
	worktree, err := repo.Toplevel()
	if err != nil {
		return err
	}
	if *title == "" {
		*title = head
	}
	pr, err := st.CreatePR(store.PR{
		Title: *title, Body: *body,
		BaseRef: *base, HeadRef: head,
		BaseSHA: baseSHA, MergeBaseSHA: mergeBase, HeadSHA: headSHA,
		WorktreePath: worktree,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created PR #%d: %s (%s -> %s)\n", pr.Number, pr.Title, pr.HeadRef, pr.BaseRef)
	return nil
}

func cmdPRList(args []string) error {
	fs := flag.NewFlagSet("pr list", flag.ContinueOnError)
	state := fs.String("state", "", "filter by state")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := openRepo()
	if err != nil {
		return err
	}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()
	prs, err := st.ListPRs(*state)
	if err != nil {
		return err
	}
	if *asJSON {
		if prs == nil {
			prs = []store.PR{}
		}
		return printJSON(prs)
	}
	if len(prs) == 0 {
		fmt.Println("no PRs")
		return nil
	}
	for _, p := range prs {
		fmt.Printf("#%-4d %-18s %-30s %s -> %s\n", p.Number, p.State, truncate(p.Title, 30), p.HeadRef, p.BaseRef)
	}
	return nil
}

func cmdPRView(args []string) error {
	fs := flag.NewFlagSet("pr view", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	pos, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	pr, st, repo, err := prByArg(pos)
	if err != nil {
		return err
	}
	defer st.Close()
	pr = refreshPR(st, repo, pr)
	if *asJSON {
		return printJSON(pr)
	}
	fmt.Printf("PR #%d: %s\n", pr.Number, pr.Title)
	fmt.Printf("  state:     %s\n", pr.State)
	fmt.Printf("  branches:  %s -> %s\n", pr.HeadRef, pr.BaseRef)
	fmt.Printf("  range:     %s..%s\n", short(pr.MergeBaseSHA), short(pr.HeadSHA))
	fmt.Printf("  worktree:  %s\n", pr.WorktreePath)
	fmt.Printf("  created:   %s\n", pr.CreatedAt)
	if pr.Body != "" {
		fmt.Printf("\n%s\n", pr.Body)
	}
	threads, err := st.ListThreads(pr.ID, false)
	if err != nil {
		return err
	}
	unresolved := 0
	for _, t := range threads {
		if !t.Resolved {
			unresolved++
		}
	}
	fmt.Printf("\n  threads: %d total, %d unresolved\n", len(threads), unresolved)
	return nil
}

func cmdPRComments(args []string) error {
	fs := flag.NewFlagSet("pr comments", flag.ContinueOnError)
	unresolved := fs.Bool("unresolved", false, "only unresolved threads")
	asJSON := fs.Bool("json", false, "output JSON (agent contract)")
	pos, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	pr, st, _, err := prByArg(pos)
	if err != nil {
		return err
	}
	defer st.Close()
	threads, err := st.ListThreads(pr.ID, *unresolved)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(threads)
	}
	if len(threads) == 0 {
		fmt.Println("no threads")
		return nil
	}
	for _, t := range threads {
		loc := "PR-level"
		if t.FilePath != "" {
			loc = fmt.Sprintf("%s:%d (%s)", t.FilePath, t.Line, t.Side)
		}
		status := "unresolved"
		if t.Resolved {
			status = "resolved"
		}
		fmt.Printf("thread %d  %s  [%s]\n", t.ID, loc, status)
		for _, c := range t.Comments {
			fmt.Printf("  [%s] %s\n", c.Author, indent(c.Body, "  "))
		}
	}
	return nil
}

func cmdPRReply(args []string) error {
	fs := flag.NewFlagSet("pr reply", flag.ContinueOnError)
	author := fs.String("author", "agent", "comment author (user|agent)")
	rest, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: lgit pr reply <thread-id> <body>")
	}
	tid, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return fmt.Errorf("bad thread id %q", rest[0])
	}
	repo, err := openRepo()
	if err != nil {
		return err
	}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()
	c, err := st.AddComment(tid, *author, strings.Join(rest[1:], " "))
	if err != nil {
		return err
	}
	fmt.Printf("added comment %d to thread %d\n", c.ID, tid)
	return nil
}

func cmdPRResolve(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: lgit pr resolve <thread-id>")
	}
	tid, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("bad thread id %q", args[0])
	}
	repo, err := openRepo()
	if err != nil {
		return err
	}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.SetThreadResolved(tid, true); err != nil {
		return err
	}
	fmt.Printf("resolved thread %d\n", tid)
	return nil
}

// cmdPRReady is the CLI twin of the MCP mark_pr_ready tool, for agents (or
// scripts) running without an MCP client.
func cmdPRReady(args []string) error {
	pr, st, _, err := prByArg(args)
	if err != nil {
		return err
	}
	defer st.Close()
	fresh, err := st.MarkPRReady(pr.ID)
	if err != nil {
		return err
	}
	fmt.Printf("PR #%d is ready for re-review (state: %s)\n", fresh.Number, fresh.State)
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 7333, "port to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := openRepo()
	if err != nil {
		return err
	}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()
	return server.New(st, repo).ListenAndServe(*port)
}

// cmdMCP serves MCP over stdio. Unlike other commands it must not require a
// repo or DB at startup: it may be registered user-wide in an MCP client and
// launched from any directory, so tools resolve (and error) per call.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return mcp.Serve(context.Background(), cwd)
}

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	rebase := fs.Bool("rebase", false, "rebase head onto base (default)")
	squash := fs.Bool("squash", false, "squash head into one commit on base")
	merge := fs.Bool("merge", false, "create a merge commit")
	cleanup := fs.Bool("cleanup", false, "remove the PR worktree and delete its branch")
	pos, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	mode := git.MergeRebase
	set := 0
	if *rebase {
		set++
	}
	if *squash {
		mode = git.MergeSquash
		set++
	}
	if *merge {
		mode = git.MergeMerge
		set++
	}
	if set > 1 {
		return fmt.Errorf("--rebase, --squash and --merge are mutually exclusive")
	}

	pr, st, repo, err := prByArg(pos)
	if err != nil {
		return err
	}
	defer st.Close()
	if pr.State == "merged" {
		return fmt.Errorf("PR #%d is already merged", pr.Number)
	}
	pr = refreshPR(st, repo, pr)

	msg := fmt.Sprintf("%s (#%d)", pr.Title, pr.Number)
	if mode == git.MergeMerge {
		msg = fmt.Sprintf("Merge PR #%d: %s", pr.Number, pr.Title)
	}
	newTip, err := repo.Merge(pr.BaseRef, pr.HeadRef, mode, msg)
	if err != nil {
		return err
	}
	if err := st.UpdatePRState(pr.ID, "merged"); err != nil {
		return err
	}
	fmt.Printf("merged PR #%d into %s (%s) at %s\n", pr.Number, pr.BaseRef, mode, short(newTip))

	if *cleanup {
		cwd, _ := os.Getwd()
		if strings.HasPrefix(cwd, pr.WorktreePath) {
			fmt.Printf("skipping cleanup: current directory is inside the PR worktree %s\n", pr.WorktreePath)
			fmt.Printf("run from another worktree: lgit merge handled; clean up with `git worktree remove %s && git branch -D %s`\n", pr.WorktreePath, pr.HeadRef)
			return nil
		}
		if err := repo.Cleanup(pr.WorktreePath, pr.HeadRef); err != nil {
			return fmt.Errorf("merge succeeded but cleanup failed: %w", err)
		}
		fmt.Printf("removed worktree %s and branch %s\n", pr.WorktreePath, pr.HeadRef)
	}
	return nil
}

// --- shared helpers ---

// parseFlagsAnywhere parses fs against args, allowing flags to appear after
// positional arguments (stdlib flag stops at the first positional). Returns
// the positional arguments in order.
func parseFlagsAnywhere(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// prByArg resolves args[0] as a PR number and returns the PR plus open
// store/repo handles. Caller closes the store.
func prByArg(args []string) (*store.PR, *store.Store, *git.Repo, error) {
	if len(args) < 1 {
		return nil, nil, nil, fmt.Errorf("missing PR number")
	}
	n, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bad PR number %q", args[0])
	}
	repo, err := openRepo()
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := openStore(repo)
	if err != nil {
		return nil, nil, nil, err
	}
	pr, err := st.GetPRByNumber(n)
	if err != nil {
		st.Close()
		return nil, nil, nil, fmt.Errorf("PR #%d: %w", n, err)
	}
	return pr, st, repo, nil
}

// refreshPR recomputes the diff range for live PRs (same logic as the server).
func refreshPR(st *store.Store, repo *git.Repo, pr *store.PR) *store.PR {
	s := server.New(st, repo)
	return s.RefreshPR(pr)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func indent(s, prefix string) string {
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}
