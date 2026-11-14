#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
REAL_TMUX=$(command -v tmux || true)
[ -n "$REAL_TMUX" ] || { printf 'skip: tmux unavailable\n'; exit 0; }

TEST_ROOT=$(mktemp -d)
SOCKET="st-env-$$"
trap '"$REAL_TMUX" -L "$SOCKET" kill-server >/dev/null 2>&1 || true; rm -rf "$TEST_ROOT"' EXIT
export HOME="$TEST_ROOT/home"
export ST_CONFIG="$TEST_ROOT/good.conf"
export ST_STATE="$TEST_ROOT/good-state"
export ST_WORKTREE_ROOT="$TEST_ROOT/good-trees"
export ST_AGENT_LOG="$TEST_ROOT/launched"
export ST_REAL_TMUX="$REAL_TMUX" ST_ISO_SOCKET="$SOCKET"
mkdir -p "$HOME/.local/bin" "$TEST_ROOT/repo"
printf 'ST_HARNESS=codex\nST_WINDOWS="agent vim shell"\n' > "$ST_CONFIG"
printf 'ST_HARNESS=claude\nST_WINDOWS="agent vim shell"\n' > "$TEST_ROOT/poison.conf"
ln -s "$ROOT/bin/st" "$HOME/.local/bin/st"

cat > "$HOME/.local/bin/tmux" <<'WRAPPER'
#!/bin/sh
exec "$ST_REAL_TMUX" -L "$ST_ISO_SOCKET" -f /dev/null "$@"
WRAPPER
for program in codex claude nvim; do
  printf '#!/bin/sh\nprintf "%%s|%%s\\n" %q "${NO_COLOR-unset}" >> "$ST_AGENT_LOG"\nexec sleep 30\n' "$program" > "$HOME/.local/bin/$program"
  chmod +x "$HOME/.local/bin/$program"
done
chmod +x "$HOME/.local/bin/tmux"

git -C "$TEST_ROOT/repo" init -q
git -C "$TEST_ROOT/repo" -c user.name=Test -c user.email=test@example.invalid \
  commit -q --allow-empty -m initial

# Start the tmux server with a different, valid config to reproduce the split
# between the parent command and processes launched later by tmux.
NO_COLOR=1 ST_CONFIG="$TEST_ROOT/poison.conf" ST_STATE="$TEST_ROOT/poison-state" \
  ST_WORKTREE_ROOT="$TEST_ROOT/poison-trees" \
  "$REAL_TMUX" -L "$SOCKET" -f /dev/null new-session -d -s sentinel 'sleep 30'

(cd "$TEST_ROOT/repo" && "$ROOT/bin/st" new feature --bare >/dev/null 2>&1)
rows=$(NO_COLOR=1 "$ROOT/bin/st" _rows)
case $rows in *$'\033'*) printf 'FAIL: st UI ignored NO_COLOR\n' >&2; exit 1;; esac
rows=$(NO_COLOR= "$ROOT/bin/st" _rows)
case $rows in *$'\033'*) ;; *) printf 'FAIL: st UI omitted color when enabled\n' >&2; exit 1;; esac
(cd "$TEST_ROOT/repo" && "$ROOT/bin/st" go feature >/dev/null 2>&1) || true

for _ in $(seq 30); do
  [ -f "$ST_AGENT_LOG" ] && grep -qx 'codex|unset' "$ST_AGENT_LOG" && grep -qx 'nvim|unset' "$ST_AGENT_LOG" && break
  sleep 0.1
done
[ -f "$ST_AGENT_LOG" ] || { printf 'FAIL: no programs launched\n' >&2; exit 1; }
grep -qx 'codex|unset' "$ST_AGENT_LOG" || { printf 'FAIL: Codex inherited NO_COLOR or did not launch\n' >&2; exit 1; }
grep -qx 'nvim|unset' "$ST_AGENT_LOG" || { printf 'FAIL: Neovim inherited NO_COLOR or did not launch\n' >&2; exit 1; }
! grep -q '^claude|' "$ST_AGENT_LOG" || { printf 'FAIL: tmux launched Claude from its own config\n' >&2; exit 1; }
[ "$("$REAL_TMUX" -L "$SOCKET" show-environment -t '=repo/feature' ST_CONFIG)" = "ST_CONFIG=$ST_CONFIG" ] ||
  { printf 'FAIL: session kept tmux server config\n' >&2; exit 1; }
[ "$("$REAL_TMUX" -L "$SOCKET" show-environment -t '=repo/feature' NO_COLOR)" = '-NO_COLOR' ] ||
  { printf 'FAIL: session did not remove server NO_COLOR\n' >&2; exit 1; }

shell_pane=$("$REAL_TMUX" -L "$SOCKET" list-panes -t '=repo/feature:shell' \
  -f '#{!=:#{@st_sidebar},1}' -F '#{pane_id}' | head -1)
[ -n "$shell_pane" ] || { printf 'FAIL: no shell pane for split\n' >&2; exit 1; }
TMUX_PANE="$shell_pane" "$ROOT/bin/st" split --vertical
"$REAL_TMUX" -L "$SOCKET" list-panes -t '=repo/feature:shell' \
  -f '#{!=:#{@st_sidebar},1}' -F '#{pane_left} #{pane_top}' |
  awk 'NR == 1 { left=$1; top=$2 } NR == 2 && $1 == left && $2 != top { stacked=1 }
       END { exit !(NR == 2 && stacked) }' ||
  { printf 'FAIL: vertical split was not stacked\n' >&2; exit 1; }

printf 'ok: isolated tmux clears NO_COLOR, launches configured programs, and stacks vertical splits\n'
