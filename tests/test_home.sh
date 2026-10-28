#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees"
export ST_SIDEBAR=0 ST_WINDOWS=shell
export ST_TEST_TMUX_STATE="$TEST_ROOT/tmux-state"
export ST_TEST_TMUX_LOG="$TEST_ROOT/tmux.log"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT" "$TEST_ROOT/repo"
git -C "$TEST_ROOT/repo" init -q
git -C "$TEST_ROOT/repo" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m initial

cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ST_TEST_TMUX_LOG"
case ${1:-} in
  has-session) [ -f "$ST_TEST_TMUX_STATE" ];;
  new-session) : > "$ST_TEST_TMUX_STATE";;
  show-options) case $* in *'@supertree_home'*) [ -f "$ST_TEST_TMUX_STATE" ] && printf '1\n';; esac;;
  display-message) case $* in *'#S'*) printf '%s\n' "${ST_TEST_CURRENT:-demo/branch}";; esac;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
cd "$TEST_ROOT/repo"
"$ROOT/bin/st"
grep -F 'new-session -d -s supertree' "$ST_TEST_TMUX_LOG" >/dev/null || fail 'bare st did not create home'
grep -F 'attach-session -t =supertree' "$ST_TEST_TMUX_LOG" >/dev/null || fail 'bare st did not enter home'
grep -Fx "$(git -C "$TEST_ROOT/repo" rev-parse --show-toplevel)" "$ST_STATE/repos" >/dev/null ||
  fail 'home did not register current repo'

: > "$ST_TEST_TMUX_LOG"
"$ROOT/bin/st" home
if grep -q '^new-session ' "$ST_TEST_TMUX_LOG"; then fail 'home created a duplicate session'; fi

: > "$ST_TEST_TMUX_LOG"
TMUX=1 "$ROOT/bin/st" leave /dev/pts/1
grep -F 'switch-client -c /dev/pts/1 -t =supertree' "$ST_TEST_TMUX_LOG" >/dev/null ||
  fail 'M-q from a tree did not return home'

: > "$ST_TEST_TMUX_LOG"
ST_TEST_CURRENT=supertree TMUX=1 "$ROOT/bin/st" leave /dev/pts/1
grep -F 'detach-client -t /dev/pts/1' "$ST_TEST_TMUX_LOG" >/dev/null ||
  fail 'M-q from home did not detach'

# Exercise the actual tmux layout when both UI dependencies are available.
real_tmux=$(command -v tmux || true)
if [ -n "$real_tmux" ] && command -v fzf >/dev/null 2>&1; then
  socket="st-home-test-$$"
  ln -s "$ROOT/bin/st" "$HOME/.local/bin/st"
  printf '#!/bin/sh\nexec %q -L %q -f /dev/null "$@"\n' "$real_tmux" "$socket" > "$HOME/.local/bin/tmux"
  chmod +x "$HOME/.local/bin/tmux"
  ST_SIDEBAR=1 TMUX_TMPDIR="$TEST_ROOT" "$ROOT/bin/st" home </dev/null > "$TEST_ROOT/home.out" 2>&1 || true
  marker=$(TMUX_TMPDIR="$TEST_ROOT" "$real_tmux" -L "$socket" show-options -qv -t '=supertree:' @supertree_home)
  [ "$marker" = 1 ] || fail 'real tmux home session has no ownership marker'
  panes=$(TMUX_TMPDIR="$TEST_ROOT" "$real_tmux" -L "$socket" list-panes -t '=supertree:shell' -F '#{@st_sidebar}')
  printf '%s\n' "$panes" | grep -qx 1 || fail 'real tmux home has no sidebar'
  TMUX_TMPDIR="$TEST_ROOT" "$real_tmux" -L "$socket" kill-server
fi

printf 'ok: home entry, reuse, and return\n'
