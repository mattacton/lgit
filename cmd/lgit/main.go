// Command lgit is a local-only PR review tool for git-worktree agent
// workflows. See docs/SPEC.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
  lgit pr base <number> <branch>                 change a PR's target branch
  lgit pr list [--state S] [--json]              list PRs
  lgit pr view <number>                          show one PR
  lgit pr comments <number> [--unresolved] [--json]
                                                 list comment threads (JSON = agent contract)
  lgit pr comment <number> <body> [--file F --line N] [--side new] [--author agent]
                                                 start a thread (omit --file for PR-level)
  lgit pr reply <thread-id> <body> [--author agent]
                                                 reply to a thread
  lgit pr resolve <thread-id>                    resolve a thread
  lgit pr review <number> --approve|--request-changes [--body text]
                                                 submit a review verdict (does not merge)
  lgit pr ready <number>                         mark a changes_requested PR ready for re-review
  lgit pr checkout <number>                      try the PR here on a detached HEAD
  lgit serve [--port 7333]                       start the web review UI + JSON API
  lgit mcp                                       serve MCP tools over stdio (agent integration)
  lgit merge <number> [--rebase|--squash|--merge] [--cleanup]
                                                 integrate the PR (default: rebase)
  lgit sweep                                     remove leftover worktrees/branches of merged PRs
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
			return fmt.Errorf("usage: lgit pr <create|base|list|view|comments|comment|reply|resolve|review|ready|checkout> ...")
		}
		switch args[1] {
		case "create":
			return cmdPRCreate(args[2:])
		case "base":
			return cmdPRBase(args[2:])
		case "list":
			return cmdPRList(args[2:])
		case "view":
			return cmdPRView(args[2:])
		case "comments":
			return cmdPRComments(args[2:])
		case "comment":
			return cmdPRComment(args[2:])
		case "reply":
			return cmdPRReply(args[2:])
		case "resolve":
			return cmdPRResolve(args[2:])
		case "review":
			return cmdPRReview(args[2:])
		case "ready":
			return cmdPRReady(args[2:])
		case "checkout":
			return cmdPRCheckout(args[2:])
		default:
			return fmt.Errorf("unknown pr subcommand %q", args[1])
		}
	case "serve":
		return cmdServe(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "merge":
		return cmdMerge(args[1:])
	case "sweep":
		return cmdSweep(args[1:])
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

// cmdPRBase is the CLI twin of the MCP set_pr_base tool. Agents (or humans)
// use it when a PR was opened against the wrong branch.
func cmdPRBase(args []string) error {
	fs := flag.NewFlagSet("pr base", flag.ContinueOnError)
	baseFlag := fs.String("base", "", "target branch (alternative to the positional argument)")
	pos, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return fmt.Errorf("usage: lgit pr base <number> <branch>")
	}
	branch := ""
	switch {
	case len(pos) == 2 && *baseFlag != "":
		return fmt.Errorf("pass the target branch once, either as an argument or --base")
	case len(pos) == 2:
		branch = pos[1]
	case *baseFlag != "":
		branch = *baseFlag
	default:
		return fmt.Errorf("usage: lgit pr base <number> <branch>")
	}
	pr, st, repo, err := prByArg(pos[:1])
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := server.New(st, repo).SetBase(pr, branch)
	if err != nil {
		return err
	}
	if res.PR.BaseRef == res.PreviousBase {
		fmt.Printf("PR #%d already targets %s\n", res.PR.Number, res.PR.BaseRef)
		return nil
	}
	fmt.Printf("updated PR #%d base: %s -> %s\n", res.PR.Number, res.PreviousBase, res.PR.BaseRef)
	if res.ApprovalCleared {
		fmt.Println("approval cleared; re-review the diff against the new target")
	}
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

func cmdPRComment(args []string) error {
	fs := flag.NewFlagSet("pr comment", flag.ContinueOnError)
	file := fs.String("file", "", "file path for an inline comment (omit for PR-level)")
	line := fs.Int("line", 0, "line number (required with --file)")
	side := fs.String("side", "new", "diff side: old or new")
	author := fs.String("author", "agent", "comment author (user|agent)")
	rest, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: lgit pr comment <number> <body> [--file path --line N]")
	}
	body := strings.Join(rest[1:], " ")
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("body is required")
	}
	filePath := *file
	lineN := int64(*line)
	switch {
	case filePath == "" && lineN == 0:
	case filePath != "" && lineN > 0:
	case filePath != "":
		return fmt.Errorf("--line is required with --file")
	default:
		return fmt.Errorf("--file is required with --line")
	}
	if *side != "old" && *side != "new" {
		return fmt.Errorf("side must be old or new")
	}
	if *author != "user" && *author != "agent" {
		return fmt.Errorf("author must be user or agent")
	}
	pr, st, repo, err := prByArg(rest[:1])
	if err != nil {
		return err
	}
	defer st.Close()
	pr = refreshPR(st, repo, pr)
	th, err := st.CreateThread(store.Thread{
		PRID: pr.ID, FilePath: filePath, Line: lineN,
		Side: *side, AnchorSHA: pr.HeadSHA,
	}, *author, body)
	if err != nil {
		return err
	}
	if filePath == "" {
		fmt.Printf("created thread %d (PR-level) on PR #%d\n", th.ID, pr.Number)
	} else {
		fmt.Printf("created thread %d on %s:%d (%s)\n", th.ID, filePath, lineN, *side)
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

func cmdPRReview(args []string) error {
	fs := flag.NewFlagSet("pr review", flag.ContinueOnError)
	approve := fs.Bool("approve", false, "approve the PR")
	request := fs.Bool("request-changes", false, "request changes")
	body := fs.String("body", "", "optional review summary")
	rest, err := parseFlagsAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 1 {
		return fmt.Errorf("usage: lgit pr review <number> --approve|--request-changes [--body text]")
	}
	verdict := ""
	set := 0
	if *approve {
		verdict = "approve"
		set++
	}
	if *request {
		verdict = "request_changes"
		set++
	}
	if set != 1 {
		return fmt.Errorf("exactly one of --approve or --request-changes is required")
	}
	pr, st, repo, err := prByArg(rest)
	if err != nil {
		return err
	}
	defer st.Close()
	rev, fresh, err := server.New(st, repo).SubmitReview(pr, verdict, *body)
	if err != nil {
		return err
	}
	fmt.Printf("reviewed PR #%d: %s (state: %s)\n", fresh.Number, rev.Verdict, fresh.State)
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

// cmdPRCheckout detaches the invoking checkout at the PR's head commit so a
// reviewer can try the code in place. `git switch --detach` never touches
// branch refs, so it works even though the PR branch is checked out in the
// agent's worktree.
func cmdPRCheckout(args []string) error {
	pr, st, repo, err := prByArg(args)
	if err != nil {
		return err
	}
	defer st.Close()
	pr = refreshPR(st, repo, pr)

	top, err := repo.Toplevel()
	if err != nil {
		return err
	}
	if samePath(top, pr.WorktreePath) {
		return fmt.Errorf("this is PR #%d's own worktree; detaching here would pull it off its branch", pr.Number)
	}
	clean, err := repo.IsClean(top)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("working tree at %s has uncommitted changes; commit or stash them first", top)
	}

	prev, err := repo.CurrentBranch()
	if err != nil {
		prev = pr.BaseRef // already detached; suggest the base branch instead
	}
	if err := repo.SwitchDetach(pr.HeadSHA); err != nil {
		return err
	}
	fmt.Printf("detached HEAD at %s (PR #%d: %s)\n", short(pr.HeadSHA), pr.Number, pr.Title)
	fmt.Printf("return with: git switch %s\n", prev)
	fmt.Println("note: commits made here will not move the PR branch")
	return nil
}

// samePath compares two paths with symlinks resolved (macOS: /var vs /private/var).
func samePath(a, b string) bool {
	return canonPath(a) == canonPath(b)
}

func canonPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
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
	// Re-root the long-running server at the primary worktree so its git
	// context (and the merge-cleanup guard) doesn't depend on the directory
	// serve happened to be launched from — e.g. a PR's own worktree.
	primary, err := repo.PrimaryWorktree()
	if err != nil {
		return err
	}
	repo = &git.Repo{Dir: primary}
	st, err := openStore(repo)
	if err != nil {
		return err
	}
	defer st.Close()
	return server.New(st, repo).ListenAndServe(*port)
}

func cmdSweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
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
	entries, err := server.New(st, repo).Sweep()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("nothing to sweep")
		return nil
	}
	failed := false
	for _, e := range entries {
		for _, a := range e.Actions {
			fmt.Printf("PR #%d: %s\n", e.PR.Number, a)
		}
		if e.Err != nil {
			failed = true
			fmt.Printf("PR #%d: error: %v\n", e.PR.Number, e.Err)
		}
	}
	if failed {
		return fmt.Errorf("sweep finished with errors")
	}
	return nil
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
	res, err := server.New(st, repo).MergePR(pr, mode, *cleanup)
	if err != nil {
		return err
	}
	fmt.Printf("merged PR #%d into %s (%s) at %s\n", res.PR.Number, res.PR.BaseRef, mode, short(res.NewTip))
	switch {
	case res.CleanedUp:
		fmt.Printf("removed worktree %s and branch %s\n", res.PR.WorktreePath, res.PR.HeadRef)
	case res.CleanupErr != nil:
		return fmt.Errorf("merge succeeded but cleanup failed: %w", res.CleanupErr)
	case res.CleanupSkipped != "":
		fmt.Println(res.CleanupSkipped)
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
