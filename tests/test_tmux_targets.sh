#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
TEST_ROOT=$(cd "$TEST_ROOT" && pwd -P)
REAL_TMUX=$(command -v tmux || true)
TEST_SOCKET="supertree-targets-$$"

cleanup() {
  [ -z "$REAL_TMUX" ] || "$REAL_TMUX" -L "$TEST_SOCKET" kill-server 2>/dev/null || true
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees"
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_HARNESS=codex\nST_WINDOWS=shell\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

fixture="$TEST_ROOT/demo"
git init -q "$fixture"
git -C "$fixture" checkout -qb main
git -C "$fixture" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m init

for branch in 'percent%x' 'hash#x' 'semi;x' "quote'x"; do
  path="$ST_WORKTREE_ROOT/demo/${branch//[^A-Za-z0-9]/-}"
  git -C "$fixture" worktree add -q -b "$branch" "$path"
done
printf '%s\n' "$fixture" > "$ST_STATE/repos"

# switch-client treats any target containing '%' as a pane target. This stub
# models live sessions and verifies that st attaches through the opaque session
# ID returned by the final ownership validation.
cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ST_TMUX_LOG"
case ${1:-} in
  list-sessions)
    case $* in
      *'#{session_id}'*)
        printf '$42\tdemo/percent%%x\n$43\tdemo/hash#x\n$44\tdemo/semi;x\n$45\tdemo/quote'"'"'x\n';;
      *)
        printf 'demo/percent%%x\ndemo/hash#x\ndemo/semi;x\ndemo/quote'"'"'x\n';;
    esac;;
  list-panes) exit 0;;
  list-clients) exit 0;;
  show-options)
    target=''; option=''
    while [ $# -gt 0 ]; do
      case $1 in
        -t) target=$2; shift 2;;
        @*) option=$1; shift;;
        *) shift;;
      esac
    done
    if [ "$option" = @supertree_label ]; then
      case $target in
        '$42'|'=demo/percent%x') printf 'demo/percent%%x\n';;
        '$43'|'=demo/hash#x') printf 'demo/hash#x\n';;
        '$44'|'=demo/semi;x') printf 'demo/semi;x\n';;
        '$45'|"=demo/quote'x") printf "demo/quote'x\n";;
      esac
    fi;;
  display-message)
    case ${!#} in
      '#{client_tty}') printf '/dev/pts/test\n';;
      '#S') printf 'outside/origin\n';;
    esac;;
  has-session) exit 0;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

export TMUX=fake
export TMUX_PANE=%99
for branch in 'percent%x' 'hash#x' 'semi;x' "quote'x"; do
  : > "$ST_TMUX_LOG"
  "$ROOT/bin/st" go "$branch"
  case $branch in
    'percent%x') expected='$42';;
    'hash#x') expected='$43';;
    'semi;x') expected='$44';;
    "quote'x") expected='$45';;
  esac
  grep -Fx "switch-client -t $expected" "$ST_TMUX_LOG" >/dev/null ||
    fail "go $branch did not use safe exact target $expected"
done
unset TMUX TMUX_PANE

# Exercise destructive targeting against a real isolated tmux server. A
# similarly named session must survive, proving the resolved target is exact.
[ -n "$REAL_TMUX" ] || {
  printf 'ok: safe exact fake tmux targets (live checks skipped; tmux is not installed)\n'
  exit 0
}
export ST_REAL_TMUX="$REAL_TMUX" ST_TEST_SOCKET="$TEST_SOCKET"
cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
exec "$ST_REAL_TMUX" -L "$ST_TEST_SOCKET" -f /dev/null "$@"
EOF
chmod +x "$HOME/.local/bin/tmux"
export PATH="$HOME/.local/bin:$PATH"

percent_session=$($ROOT/bin/st _sessions | grep -Fx 'demo/percent%x')
[ -n "$percent_session" ] || fail 'percent session was not listed'
tmux new-session -d -s "$percent_session" -n shell
tmux set-option -q -t "$percent_session" @supertree_label 'demo/percent%x'
tmux new-session -d -s "$percent_session-extra" -n shell
"$ROOT/bin/st" down "$percent_session"
if tmux has-session -t "=$percent_session" 2>/dev/null; then
  fail 'percent session was not closed by ID'
fi
tmux has-session -t "=$percent_session-extra" 2>/dev/null ||
  fail 'exact targeting closed the similarly named session'

for branch in 'hash#x' 'semi;x' "quote'x"; do
  session=$($ROOT/bin/st _sessions | grep -Fx "demo/$branch")
  tmux new-session -d -s "$session" -n shell
  tmux set-option -q -t "$session" @supertree_label "demo/$branch"
  "$ROOT/bin/st" down "$session"
  if tmux has-session -t "=$session" 2>/dev/null; then
    fail "metacharacter session remained open: $session"
  fi
done

printf 'ok: safe exact tmux targets for percent and shell metacharacters\n'
