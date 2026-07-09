# lgit — Local-Only PR Review for Agent Worktrees

> Source of truth for the design. Status: Phase 1 (MVP core loop) complete; Phase 2 (agent integration) in progress — MCP server shipped; remaining: changes-since-last-review, outdated-thread detection, new-commit notification.

## Context

When running multiple coding agents on one codebase, each agent works in its own
git worktree on its own branch. We want a **local-only** way to turn each
worktree's branch into a reviewable "PR": review it in a rich UI (better than the
plain diffs in Lumen/LazyGit), leave inline comments, have the agent read those
comments and fix the issues, then merge into `main`. GitHub stays as the remote;
this is purely a local review layer.

**Why build (web search, June 2026):** the closest existing tools —
[diffx](https://github.com/wong2/diffx) and [difit](https://github.com/yoshiko-pg/difit) —
are excellent *diff-review surfaces* but not *PR forges*: comments are ephemeral /
client-side, there's no persistent multi-PR store, no merge, and worktrees are
unaddressed. Agent orchestrators (Composio, Claude Code agent teams) hand each
agent a worktree but route review through *real GitHub* PRs. No tool does
persistent, multi-PR, **worktree-native lifecycle with merge, locally**. `lgit`
fills exactly that gap and may borrow UI structure from diffx (verify its license).

**Outcome:** a single `lgit` Go binary providing a CLI, a local web review UI, and
an MCP server, sharing one SQLite store visible from every worktree.

## Decisions locked

- **Storage:** local SQLite at the shared git dir. Not git-native/pushable; local-only.
- **Review UI:** browser only (no TUI).
- **Default merge:** `--rebase` (linear history); `--squash` / `--merge` available as flags.
- **No TS-first prototype.** Backend written in Go from day one; frontend is JS regardless.

## Stack

- **Backend / distribution:** Go (single binary, `go1.26`). Git via `os/exec`
  (shell out to `git`; `go-git` reserved for later if pure-Go plumbing is wanted).
  SQLite via pure-Go `modernc.org/sqlite` (no cgo → trivial cross-compile). HTTP via
  stdlib `net/http`. Frontend assets embedded with `embed.FS`.
- **Frontend:** React + Vite + **Shiki** (highlighting) + a diff renderer
  (`react-diff-view` or `diff2html`). Built into `internal/server/dist`, embedded.
- **MCP:** Go SDK — evaluate official `modelcontextprotocol/go-sdk` vs
  `mark3labs/mcp-go`; confirm tools + stdio transport coverage before committing.

## Storage & schema

Single SQLite DB at `$(git rev-parse --git-common-dir)/lgit/lgit.db`. All worktrees
of a repo share the same `--git-common-dir`, so one DB is visible from every
worktree — no per-worktree config. `lgit init` creates the dir + schema.

Schema (v1):

- `pr(id, number, title, body, base_ref, head_ref, base_sha, merge_base_sha,
  head_sha, worktree_path, state, created_at, updated_at)`
  - `state ∈ {open, changes_requested, approved, merged, closed}`
- `review(id, pr_id, verdict, body, created_at)` — `verdict ∈ {approve, request_changes, comment}`; powers "changes since last review."
- `thread(id, pr_id, file_path, line, side, anchor_sha, resolved, outdated, created_at)`
  - `side ∈ {old, new}`; `anchor_sha` = the head_sha the comment was made against.
- `comment(id, thread_id, author, body, created_at)` — `author ∈ {user, agent}`.

PR diff range = `merge_base_sha..head_sha` against base, recomputed on refresh so
new agent commits appear.

## Surfaces

### 1. CLI (`lgit`)
- `lgit init` — create DB/schema at the common git dir.
- `lgit pr create [--title] [--base main]` — from the current worktree: detect branch
  (`head_ref`), compute `merge-base` with base, snapshot SHAs, insert PR.
- `lgit pr list [--state] [--json]`
- `lgit pr view <id>` / `lgit pr comments <id> [--unresolved] [--json]` — JSON is the
  agent-facing contract.
- `lgit serve [--port]` — start web UI + API (+ optionally MCP over HTTP).
- `lgit pr resolve <thread-id>` / `lgit pr reply <thread-id> <body>` — also for agents.
- `lgit pr ready <id>` — CLI twin of MCP `mark_pr_ready` (changes_requested → open),
  for agents running without an MCP client.
- `lgit merge <id> [--rebase|--squash|--merge] [--cleanup]` — integrate head into base
  (**default `--rebase`**), set state=merged; `--cleanup` runs `git worktree remove`
  + `git branch -d`.

### 2. Web review UI (`lgit serve` → localhost)
PR dashboard → PR detail with: side-by-side + unified diff, Shiki highlighting, file
tree with filter, collapse-unchanged, intra-line word diff; inline comment threads
(`+` on any line) + PR-level comments + resolve; "viewed" checkboxes;
**"changes since last review"** (diff only commits added since the last `review` row);
Approve / Request changes (writes a `review` row, updates `pr.state`); outdated-thread
badge when `anchor_sha` ≠ current `head_sha` and the anchored line changed (detect
only, no auto-migrate in v1).

### 3. MCP server
`lgit mcp` (stdio, official `modelcontextprotocol/go-sdk`): `list_prs`, `get_pr`,
`get_unresolved_comments(pr)`, `reply_comment(thread, body)`,
`resolve_comment(thread)`, `mark_pr_ready(pr)`. `mark_pr_ready` flips
`changes_requested` → `open` (no new state; the review history distinguishes
re-reviews). Repo/store are resolved per tool call so the server starts cleanly
outside lgit repos (safe for user-wide registration). HTTP-transport MCP via
`lgit serve` deferred until needed.

## The loop

1. Agent finishes in worktree → `lgit pr create`.
2. User reviews in `lgit serve`, leaves inline comments, clicks Request changes.
3. Agent reads unresolved comments via MCP/`--json`, fixes, commits, replies, resolves,
   calls `mark_pr_ready`.
4. User re-reviews via "changes since last review", approves.
5. `lgit merge <id> --cleanup` → rebased onto `main`, worktree + branch removed.

## Phases

- **Phase 1 — MVP core loop:** `init`, `pr create` (worktree-aware), `serve` with
  side-by-side diff + inline comments + resolve + approve/request-changes,
  `pr comments --json`, `merge` with cleanup. Schema v1.
- **Phase 2 — agent integration:** MCP server; "changes since last review";
  outdated-thread detection; notification when new commits land on an open PR's head.
- **Phase 3 — polish:** difftastic/AST-diff toggle; stacked PRs; "promote to GitHub"
  via `gh pr create`.

## Layout

```
lgit/
  docs/SPEC.md          # this document
  go.mod                # module lgit, go 1.26
  cmd/lgit/main.go      # CLI entry
  internal/store/       # SQLite schema + queries (modernc.org/sqlite)
  internal/git/         # worktree detection, merge-base, diff range, merge/cleanup
  internal/server/      # net/http API + embed.FS for built web assets
  internal/mcp/         # MCP tools (Phase 2)
  web/                  # React + Vite + Shiki source; built into internal/server/dist
```

## Implementation notes (gotchas / reuse)

- **Worktree discovery:** `git worktree list --porcelain`; current worktree via
  `git rev-parse --show-toplevel`; common dir via `git rev-parse --git-common-dir`.
- **Diff range:** `git diff <merge_base>..<head> --`; anchor via
  `git merge-base <base> <head>`. Recompute on each PR refresh.
- **Merge from a worktree (default rebase):** operate on the shared repo without
  disturbing any worktree's working tree — use a temporary detached worktree of base
  (`git worktree add --detach <tmp> <base>`), replay head onto it
  (`git rebase --onto`, or `git merge --squash` for the squash option), move the base
  branch ref to the result, remove the temp worktree.
- **License check:** review `wong2/diffx` (and `difit`) licenses before borrowing any
  frontend structure.

## Verification

- **Unit:** `internal/git` against a temp repo with worktrees (`git init`, branch,
  `worktree add`, commits) — assert merge-base / diff-range / merge. `internal/store`
  CRUD round-trips.
- **End-to-end manual:**
  1. Scratch repo, `git worktree add ../wt-feature feature`, make commits.
  2. `lgit init`, then from the worktree `lgit pr create --title "test"`.
  3. `lgit serve`, open browser, confirm diff renders, add inline comment, request changes.
  4. `lgit pr comments <id> --unresolved --json` returns the comment with file:line.
  5. `lgit pr reply` + `lgit pr resolve`; confirm UI + "changes since last review" after a new commit.
  6. `lgit merge <id> --cleanup`; confirm `main` has the change and worktree/branch are gone.
- **MCP (Phase 2):** point a Claude Code agent at the `lgit` MCP server; confirm it can
  list PRs, read unresolved comments, reply, and resolve.
