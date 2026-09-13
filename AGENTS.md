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

## Git hooks

Local hooks run through [lefthook](https://github.com/evilmartians/lefthook), a
single static binary (this repo has no package-manager hook install).

**AI agents**: do not install the lefthook binary yourself — it is included in the OpenCode image. If `lefthook` is not on `PATH`, report this to the user and ask whether to install it.

Human contributors install it once per clone, then register the hooks:

```sh
curl -fsSL -o /tmp/lefthook.gz \
  https://github.com/evilmartians/lefthook/releases/download/v2.1.12/lefthook_2.1.12_Linux_x86_64.gz
gunzip /tmp/lefthook.gz && chmod +x /tmp/lefthook && mv /tmp/lefthook ~/.local/bin/
# (arm64/macOS: pick the matching `lefthook_2.1.12_<OS>_<ARCH>` release asset)
PATH="$HOME/.local/bin:$PATH" lefthook install   # idempotent; re-run after a fresh clone
```

`lefthook.yml` pins the shared `MartinCa/lefthook-configs` fragments at `v2.1.0`:
- **pre-commit** — `langs/go.yml` runs `gofmt -w` and `goimports -w` on staged
  `*.go` (re-staging fixed files); `lefthook-shared.yml` secret-scans the staged
  diff with `betterleaks` (blocks the commit on a leak) and audits staged
  `.github/workflows/*` files with `zizmor` (blocks on a finding).
- **pre-push** — `pre-push-go.yml` runs the full test suite (`go test ./...`) on
  every push, blocking pushes on a red suite. It needs the `go` toolchain on
  `PATH` (always true in this repo). Escape hatches: `git push --no-verify`
  (bypasses all hooks for that push) or `LEFTHOOK=0 git push` (bypasses lefthook
  only).
- **commit-msg** — `commit-msg.yml` enforces Conventional Commits, e.g.
  `feat: ...`, `fix(api): ...`.

`go build` and `go test` are enforced in `ci.yml` only (the `lint` job runs `go
build ./...` and `test -z "$(gofmt -l .)"`; the `test` job runs `go test ./...`).
The hooks run formatting only: `gofmt -w` and `goimports -w`. `goimports` (import
grouping and ordering), `betterleaks`, and the commit-msg check are **hook-only** —
CI does not run them, so the pre-commit hook is the only guard. `zizmor` runs in
both places but in CI it only uploads a SARIF report to code scanning
(non-blocking, not a merge gate); the pre-commit hook is the blocking check.

Two hook tools must be on `PATH`: `betterleaks` (secret scan, install per its
project README) and `zizmor` (workflow audit, install from zizmor.sh). If a tool
is missing, `LEFTHOOK=0 git commit` skips the hooks entirely — a pragmatic escape
hatch for restricted setups, not a way to dodge the gates. `lefthook dump` shows
the merged hook config; `lefthook run pre-commit --all-files` verifies it.
