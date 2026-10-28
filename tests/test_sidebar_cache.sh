#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees"
export ST_GIT_LOG="$TEST_ROOT/git.log"
export ST_REAL_GIT
ST_REAL_GIT=$(command -v git)
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT" "$TEST_ROOT/repo"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
"$ST_REAL_GIT" -C "$TEST_ROOT/repo" init -q
"$ST_REAL_GIT" -C "$TEST_ROOT/repo" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m initial
"$ST_REAL_GIT" -C "$TEST_ROOT/repo" rev-parse --show-toplevel > "$ST_STATE/repos"

cat > "$HOME/.local/bin/git" <<'EOF'
#!/bin/sh
case $* in *'worktree list --porcelain'*) printf 'list\n' >> "$ST_GIT_LOG";; esac
exec "$ST_REAL_GIT" "$@"
EOF
cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/bin/sh
case $* in *'display-message'*'#S'*) printf 'supertree\n';; esac
EOF
chmod +x "$HOME/.local/bin/git" "$HOME/.local/bin/tmux"

"$ROOT/bin/st" _sidebar-rows >/dev/null
"$ROOT/bin/st" _sidebar-rows >/dev/null
[ "$(wc -l < "$ST_GIT_LOG" | tr -d ' ')" = 1 ] || {
  printf 'FAIL: home sidebar rebuilt an initialized empty cache\n' >&2; exit 1;
}

: > "$ST_STATE/sidebar/stale"
"$ROOT/bin/st" _sidebar-rows >/dev/null
[ "$(wc -l < "$ST_GIT_LOG" | tr -d ' ')" = 2 ] || {
  printf 'FAIL: stale home sidebar did not refresh\n' >&2; exit 1;
}

printf 'ok: home sidebar cache reuse and stale refresh\n'
