# lgit — local-only PR review for git worktrees

`lgit` turns a git worktree's branch into a reviewable "PR" that you review in a
local browser UI — side-by-side diffs, Shiki highlighting, inline comment threads —
without ever touching a remote. An agent working in the worktree then reads those
comments as JSON, fixes the code, replies, and resolves; you re-review and merge.
GitHub stays as the remote; this is purely a local review layer.

The whole thing ships as a single Go binary with the web UI embedded inside it.
See [`docs/SPEC.md`](docs/SPEC.md) for the full design.

## Requirements

- **Go 1.26+** (`go.mod` pins `go 1.26.2`)
- **Node 18+ / npm** — only needed to rebuild the web UI (see [Building](#building))
- **git** — `lgit` shells out to the `git` CLI

SQLite is provided by the pure-Go `modernc.org/sqlite`, so there is no cgo and no
system SQLite dependency.

## Layout

```
cmd/lgit/            CLI entry point
internal/git/        worktree detection, merge-base, diff range, merge/cleanup
internal/store/      SQLite schema + queries (modernc.org/sqlite)
internal/server/     net/http JSON API + embedded web UI
internal/mcp/        MCP server (official modelcontextprotocol/go-sdk, stdio)
internal/server/dist built web assets, embedded via embed.FS (checked in — see below)
web/                 React + Vite + Shiki frontend source
docs/SPEC.md         design doc
```

## Building

The binary embeds the built web UI, so a full build has two stages: build the
frontend, then build the Go binary.

### 1. Build the web UI → `internal/server/dist`

```bash
cd web
npm install          # first time only
npm run build        # vite build → ../internal/server/dist
```

Vite is configured (`web/vite.config.js`) to emit its output straight into
`internal/server/dist` with `emptyOutDir: true`, replacing the previous build.

**`internal/server/dist` is committed to git on purpose.** `internal/server/server.go`
embeds it with `//go:embed all:dist`, so the directory must exist on disk at compile
time for the binary to serve the UI. This is why `.gitignore` excludes `web/dist/`
but *tracks* `internal/server/dist/`.

The practical consequence: **whenever you change anything under `web/`, re-run
`npm run build` and commit the regenerated `internal/server/dist/` along with your
source changes.** If you don't, the Go binary will embed and serve a stale UI. If
the `dist` folder is missing entirely, the server returns
`UI assets not embedded; rebuild with npm run build in web/`.

### 2. Build the Go binary

```bash
go build -o lgit ./cmd/lgit
```

This produces the `lgit` binary in the repo root (it's gitignored). With Go's build
cache you only pay the frontend cost when the UI actually changes; a backend-only
edit just needs this step.

### Frontend development

For iterating on the UI without rebuilding the binary each time:

```bash
# terminal 1: run the API/backend
lgit serve                 # listens on :7333

# terminal 2: run the Vite dev server with hot reload
cd web && npm run dev
```

`web/vite.config.js` proxies `/api` to `http://127.0.0.1:7333`, so the dev server
talks to a running `lgit serve`. When you're done, run `npm run build` to bake your
changes into `internal/server/dist` and commit it.

## Running

`lgit` stores everything in one SQLite DB at
`$(git rev-parse --git-common-dir)/lgit/lgit.db`. Because every worktree of a repo
shares the same common git dir, that single DB is visible from all of them — no
per-worktree config.

The review loop:

```bash
# once per repo
lgit init                              # create the DB + schema

# from inside a feature worktree
lgit pr create --title "My change" --base main

# review in the browser
lgit serve                             # http://127.0.0.1:7333  (--port to change)
lgit pr checkout <number>              # try the branch here (detached HEAD); return with `git switch main`

# the agent side (also how you script it)
lgit pr list --state open
lgit pr comments <number> --unresolved --json     # the agent-facing contract
lgit pr reply <thread-id> "Fixed." --author agent
lgit pr resolve <thread-id>
lgit pr ready <number>                 # after addressing feedback: changes_requested -> open

# integrate (or click "Rebase & merge" in the web UI)
lgit merge <number> --cleanup          # default --rebase; --squash / --merge available
lgit sweep                             # finish any leftover cleanup for merged PRs
```

Run `lgit help` for the full command list.

## MCP server (agent integration)

`lgit mcp` serves the agent side of the review loop as MCP tools over stdio:
`list_prs`, `get_pr`, `get_unresolved_comments`, `reply_comment`,
`resolve_comment`, and `mark_pr_ready` (flips a `changes_requested` PR back to
`open` once the agent has addressed feedback, signalling it's the reviewer's
turn again).

To register it in Claude Code for yourself across all repos — without adding
any config to the repo or affecting other devs:

```bash
claude mcp add --scope user lgit -- lgit mcp
```

(Use `--scope project` instead if a team explicitly wants a shared `.mcp.json`.)

A user-wide registration is safe because the server starts from any directory
and resolves the repo *per tool call*: outside a git repo, or in one where
`lgit init` hasn't been run, the server still starts cleanly and each tool
returns an error saying so. Since all worktrees of a repo share one lgit DB,
agents in different worktrees all see the same PRs.

## Testing

Go unit tests cover the git plumbing (against a temp repo with real worktrees),
the store CRUD, the HTTP API (`httptest` against real scratch repos), and the MCP
tools (in-process MCP client over in-memory transports):

```bash
go build ./... && go vet ./... && go test ./...
```

There are no frontend unit tests; the UI is verified manually via the end-to-end
flow described in [`docs/SPEC.md`](docs/SPEC.md#verification) (create a scratch repo
and worktree, `lgit pr create`, `lgit serve`, add a comment, reply/resolve, then
`lgit merge --cleanup`).
