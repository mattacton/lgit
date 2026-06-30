# lgit

Local-only, GitHub-style PR review tool for git-worktree workflows: a human reviews
each worktree's branch in a browser (`lgit serve`), leaves inline comments, and merges.
See `docs/SPEC.md` for the design and `README.md` for usage.

## Build / test

- `go build ./... && go vet ./... && go test ./...`
- Frontend is in `web/` (React + Vite + Shiki). Rebuild the embedded UI with
  `cd web && npm run build`; the output `internal/server/dist/` is tracked and baked
  into the binary via `//go:embed`, so rebuild it after changing anything under `web/`.

## Working agreement for agents

Do feature work in an isolated git worktree, and open a local lgit PR when it's done so
a human can review it. This is what keeps parallel agents from colliding and keeps each
change reviewable on its own.

1. **Start in a worktree.** For any non-trivial task, work in a dedicated worktree on a
   feature branch off `main` — use the EnterWorktree tool, or
   `git worktree add -b <branch> ../lgit-<branch> main`. Branch from `main` so the PR
   diff is exactly your change.
2. **Commit on that branch** and get the checks above green. Don't push.
3. **Open a local PR when done:** `lgit init` (idempotent) then
   `lgit pr create --title "<concise summary>"`. Report the PR number; it's reviewable
   with `lgit serve` → http://localhost:7333.
4. **Act on review feedback:** `lgit pr comments <n> --unresolved --json` → make the
   fixes → `lgit pr reply <thread-id> "<what changed>" --author agent` and
   `lgit pr resolve <thread-id>`. Never resolve a thread you didn't actually address.
5. **Don't merge your own PR** — merging is the reviewer's call.

The `lgit-open-pr` and `lgit-review` skills automate steps 1–5; they trigger
automatically, so you usually don't need to invoke anything by hand.
