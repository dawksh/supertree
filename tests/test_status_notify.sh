#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
export HOME="$TEST_ROOT/home" ST_STATE="$TEST_ROOT/state" ST_CONFIG="$TEST_ROOT/config"
export ST_NOTIFY_LOG="$TEST_ROOT/notify.log"
mkdir -p "$HOME/.local/bin" "$ST_STATE"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

for backend in osascript notify-send; do
  cat > "$HOME/.local/bin/$backend" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >> "$ST_NOTIFY_LOG"
EOF
  chmod +x "$HOME/.local/bin/$backend"
done
export PATH="$HOME/.local/bin:$PATH"

ST_NOTIFY=1 "$ROOT/bin/st" config | grep -qx $'notify\ton' || fail 'config omitted enabled notifications'
"$ROOT/bin/st" config | grep -qx $'notify\toff' || fail 'config omitted disabled notifications'

real_tmux=$(command -v tmux || true)
if [ -n "$real_tmux" ]; then
  export TMUX_TMPDIR="$TEST_ROOT"
  socket="st-status-test-$$"
  "$real_tmux" -L "$socket" -f /dev/null new-session -d -s 'repo/main'
  trap '"$real_tmux" -L "$socket" kill-server 2>/dev/null || true; rm -rf "$TEST_ROOT"' EXIT
  tmux() { "$real_tmux" -L "$socket" "$@"; }
  st_register_command() { :; }
  # The modules register their commands at source time; these functions are
  # exercised against an isolated server with no user's sessions or config.
  . "$ROOT/bin/st-lib/05-ui.sh"
  . "$ROOT/bin/st-lib/50-tmux.sh"
  . "$ROOT/bin/st-lib/55-sidebar.sh"

  export ST_SIDEBAR_WIDTH=24
  style_session 'repo/main'
  tmux set-option -t '=repo/main:' @supertree_label 'repo/main'
  tmux set-option -t '=repo/main:' @st_state_glyph "$G_IN"
  left=$(tmux show-options -v -t '=repo/main:' status-left)
  rendered=$(tmux display-message -p -t '=repo/main:' -F "$left")
  case $rendered in *"$G_IN repo/main"*) ;; *) fail 'status-left omitted tree and state';; esac
  [ "$(tmux show-options -v -t '=repo/main:' status-left-length)" = 25 ] ||
    fail 'status-left no longer aligns with default sidebar width'

  tmux set-option -t '=repo/main:' @supertree_label 'webauth/a-very-long-feature-branch-name'
  rendered=$(tmux display-message -p -t '=repo/main:' -F "$left")
  case $rendered in *'webauth/a-…ranch-name'*) ;; *) fail 'long tree label did not keep both ends';; esac

  ST_SIDEBAR_WIDTH=30 style_session 'repo/main'
  [ "$(tmux show-options -v -t '=repo/main:' status-left-length)" = 31 ] ||
    fail 'status-left no longer follows a custom sidebar width'

  old=$(printf 'repo\tmain\trunning\t/tree\trepo/main')
  new=$(printf 'repo\tmain\tinput\t/tree\trepo/main')
  ST_NOTIFY=1 status_notices "$old" "$new"
  grep -qx 'repo/main needs you' "$ST_NOTIFY_LOG" || fail 'input transition sent no desktop notice'
  [ "$(grep -cx 'repo/main needs you' "$ST_NOTIFY_LOG")" = 1 ] ||
    fail 'input transition sent duplicate desktop notices'
  ST_NOTIFY=1 status_notices "$new" "$new"
  ST_NOTIFY=0 status_notices "$old" "$new"
  [ "$(grep -cx 'repo/main needs you' "$ST_NOTIFY_LOG")" = 1 ] ||
    fail 'unchanged or disabled notification sent a desktop notice'
fi

printf 'ok: status-left alignment, middle truncation, and desktop notices\n'
