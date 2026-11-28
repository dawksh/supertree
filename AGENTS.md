# Agent guide

## Project context

Supertree (`st`) manages Git worktrees and their tmux sessions, with coding-agent, editor, and shell windows. The shell CLI is the default product; the Go workspace core and its integrations are optional. Read the relevant sections of `README.md` before changing behavior. Plans and handoff documents provide background, but check the current code and README before treating a proposed feature as implemented.

The released v0.4.34 workflow is the compatibility baseline: preserve existing key bindings, picker actions, configured window order, agent resume behavior, and removal confirmations unless the task explicitly changes them. The actual agent harness owns its conversation and approvals. New controls and integrations should remain optional, and ordinary workspace launches must not silently enable the Go core or agent tools. Preserve running programs and user state across changes, upgrades, and recovery.

## Where things live

- `bin/st` and `bin/st-lib/`: Bash CLI and its modules.
- `tmux/supertree.conf` and `editor/supertree.lua`: tmux and editor integration.
- `cmd/st-next/` and `internal/`: Go client, daemon, commands, workspace state, tmux integration, TUI, and related services.
- `install.sh` and `scripts/`: installation, packaging, release, and local test tooling.
- `tests/`: shell and integration tests; Go tests live alongside their packages.
- `dev`: isolated local development launcher. `.dev/` is local state, not source.

Saving a desired layout currently records metadata; it does not change live tmux panes. Check the implementation as this feature evolves.

## Working conventions

- Inspect nearby code and existing tests first; make focused changes that fit the existing design. Keep interfaces and observable behavior stable unless a change is requested.
- Use idiomatic, `gofmt`-formatted Go. Follow existing package boundaries and return useful errors. For Bash, follow the surrounding style, quote expansions, preserve arguments and exit status, and consider paths containing spaces, quotes, and tmux format characters.
- Support macOS and Linux. Avoid assuming GNU-only utilities or changing users' dotfiles, normal tmux server, worktrees, or installed release as a side effect of development and tests.
- Treat commands, environment variables, config, persisted state, JSON/RPC schemas, installer behavior, and release assets as compatibility surfaces. Validate external input and avoid exposing secrets in command lines, tmux metadata, logs, or errors.
- Add or update focused behavioral tests for meaningful changes, including failure and compatibility cases where relevant. Prefer tests of observable behavior over implementation details. Update the README or other relevant documentation when user-facing behavior or setup changes.
- Respect existing uncommitted changes. Do not overwrite unrelated work or perform destructive Git operations. Keep generated binaries and `.dev/` state out of source changes.
- Use Conventional Commits for commit messages: `<type>(<optional scope>): <description>`, for example `feat: add workspace search`, `fix: preserve window order`, or `chore: update agent guidance`. Choose a type that describes the change, such as `feat`, `fix`, `docs`, `refactor`, `test`, or `chore`; use `!` and a `BREAKING CHANGE:` footer when appropriate for breaking changes.
- Communicate clearly and directly: explain the outcome, relevant tradeoffs, tests run, and any remaining limitations. Do not claim a preference or project guarantee that the available evidence does not support.

## Development and verification

Use `./dev open` to try the checkout in its isolated workspace; `./dev build` rebuilds the Go core. An already running daemon must be stopped and started to load changed core code. `./dev package` builds local release assets without publishing.

Run tests through the isolated runner: it supplies a temporary HOME and redirects tmux sockets so tests cannot affect the user's normal tmux server. Do not run tmux integration tests against the normal environment.

- Full checks: `./dev test` (Go vet, race tests, shell syntax and suite).
- Focused Go check: `bash scripts/test-local.sh -- go test -race ./internal/process` (substitute the relevant package).
- Shell suite: `bash scripts/test-local.sh --shell`; a focused shell test can run via `bash scripts/test-local.sh -- bash tests/test_next.sh`.
- Go formatting: `gofmt` on changed Go files; CI checks `gofmt -l cmd internal`.

Choose checks appropriate to the change and report what actually ran. CI runs Go and shell checks on macOS and Linux; local automated checks do not replace interactive terminal or SSH verification when those are relevant.
