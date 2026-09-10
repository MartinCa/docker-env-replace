# Agent instructions

Guidance for AI coding agents (and humans) working in this repository.

## Scratch work and cleanup — avoid `rm -rf`

Do not use `rm -rf` for scratch-file management. It triggers a permission
prompt for every invocation (`rm -rf*` is configured as `ask`) and a
hardcoded `rm -rf` path is an unrecoverable-mistake risk.

Instead:

- Use unique scratch directories: `d=$(mktemp -d /tmp/opencode/<name>-XXXX)`.
  A fresh `mktemp -d` directory is empty by construction, so there is
  nothing to delete beforehand.
- Do **not** clean scratch dirs up with shell commands during a session.
  Leave them; `/tmp` is ephemeral and cleaned by the OS.
- If cleanup is genuinely required (e.g. large artifacts), `rm -rf` with a
  **literal path under `/tmp/opencode/`** is allow-listed in the opencode
  permission config and will not prompt. Variable-based cleanup
  (`rm -rf "$d"`) still prompts — the permission matcher sees the raw
  command text before expansion.
- Never use `rm -rf` on hardcoded repository or workspace paths, and never
  hide `rm -rf` inside strings (e.g. `trap 'rm -rf …' EXIT`) — that
  bypasses the permission guard and must not be relied on.

## Prefer tests over ad-hoc shell experiments

The Go test suite (`go test ./...`) covers the utility's behavior. For
anything reusable, extend `main_test.go` (use `t.TempDir()`, which cleans
up automatically) instead of hand-rolling shell smoke tests.

## Repo conventions

- Go toolchain: `go vet ./...`, `gofmt -l .` (must be empty), and
  `go test ./...` must all pass before committing.
- CI workflows use SHA-pinned actions with version comments and minimal
  permissions — keep it that way for any new workflow.
