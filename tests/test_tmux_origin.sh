#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
TEST_ROOT=$(cd "$TEST_ROOT" && pwd -P)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees"
export ST_TEST_LOG="$TEST_ROOT/tmux.log"
export ST_TEST_TTY='/dev/pts/42'
export ST_TEST_CURRENT_SESSION=outside
export ST_TEST_WINDOW_ID='@7'
export TMUX=mock
export TMUX_PANE='%42'
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_HARNESS=codex\nST_WINDOWS=shell\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

assert_log_contains() {
  grep -Fx -- "$1" "$ST_TEST_LOG" >/dev/null || fail "tmux log did not contain: $1"
}

cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ST_TEST_LOG"
case ${1:-} in
  display-message)
    format=${!#}
    case $format in
      '#{client_tty}') printf '%s\n' "$ST_TEST_TTY";;
      '#S') printf '%s\n' "$ST_TEST_CURRENT_SESSION";;
      '#{window_id}') printf '%s\n' "$ST_TEST_WINDOW_ID";;
    esac
    ;;
  has-session) exit 0;;
  list-panes) exit 0;;
  list-clients)
    [ -n "${ST_TEST_CLIENTS:-}" ] && printf '%s\n' "$ST_TEST_CLIENTS"
    ;;
  list-sessions) printf '1|fallback\n';;
  switch-client)
    if [ "${ST_TEST_FAIL_SAVED_WINDOW:-0}" = 1 ] &&
       [[ $* == *':@'* ]]; then
      exit 1
    fi
    ;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

repo="$TEST_ROOT/demo"
git init -q "$repo"
git -C "$repo" checkout -qb main
git -C "$repo" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m init
git -C "$repo" worktree add -q -b feature "$ST_WORKTREE_ROOT/demo/feature"
printf '%s\n' "$repo" > "$ST_STATE/repos"
feature_session=$("$ROOT/bin/st" _sessions | grep '/feature$')
[ -n "$feature_session" ] || fail 'feature session was not listed'

# Entering a tree records both the outside session and its exact window.
: > "$ST_TEST_LOG"
"$ROOT/bin/st" go feature
origin_file="$ST_STATE/origin/-dev-pts-42"
[ "$(cat "$origin_file")" = $'outside\t@7' ] || fail 'origin did not include the window ID'
assert_log_contains "switch-client -t =$feature_session"

# Even if the outside session changed windows while we were away, leave uses
# the saved ID rather than that session's newly active window.
export ST_TEST_CURRENT_SESSION="$feature_session"
export ST_TEST_WINDOW_ID='@8'
: > "$ST_TEST_LOG"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
assert_log_contains 'switch-client -c /dev/pts/42 -t =outside:@7'
if grep -q '^detach-client ' "$ST_TEST_LOG"; then
  fail 'leave detached after restoring the saved window'
fi

# Closing the saved window degrades to the historical session-level restore.
printf 'outside\t@7\n' > "$origin_file"
: > "$ST_TEST_LOG"
ST_TEST_FAIL_SAVED_WINDOW=1 "$ROOT/bin/st" leave "$ST_TEST_TTY"
assert_log_contains 'switch-client -c /dev/pts/42 -t =outside:@7'
assert_log_contains 'switch-client -c /dev/pts/42 -t =outside'

# State written by earlier releases is still accepted.
printf 'outside\n' > "$origin_file"
: > "$ST_TEST_LOG"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
assert_log_contains 'switch-client -c /dev/pts/42 -t =outside'
if grep -q ':@' "$ST_TEST_LOG"; then fail 'legacy state invented a window target'; fi

# Destructive session operations evacuate clients to the saved window too.
printf 'outside\t@7\n' > "$origin_file"
: > "$ST_TEST_LOG"
ST_TEST_CLIENTS="$ST_TEST_TTY" "$ROOT/bin/st" down feature -y >/dev/null
assert_log_contains "list-clients -t =$feature_session -F #{client_tty}"
assert_log_contains 'switch-client -c /dev/pts/42 -t =outside:@7'
assert_log_contains "kill-session -t =$feature_session"

printf 'ok: exact origin windows, stale-window fallback, and legacy origin state\n'
