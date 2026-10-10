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
export ST_TEST_PICKER_INPUT="$TEST_ROOT/picker.txt"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ST_TEST_LOG"
case ${1:-} in
  has-session)
    if [ "${ST_TEST_MAIN_CLOSED:-0}" = 1 ] &&
       [[ $* == *"=$ST_TEST_MAIN_SESSION"* ]]; then exit 1; fi
    [ "${ST_TEST_HAS_SESSIONS:-0}" = 1 ]; exit;;
  set-option) case $* in *'-t =demo-'*) exit 1;; esac;;
  show-options)
    case $* in
      *'@st_agent_window'*) printf 'codex\n';;
      *'@st_agent_state'*) printf '%s\n' "${ST_TEST_AGENT_STATE:-running}";;
    esac;;
  list-panes)
    if [ "${2:-}" = -a ]; then
      printf '%s\n' "${ST_TEST_PANES:-}"
    else
      printf '%%0\n'
    fi;;
  capture-pane) printf '%s\n' "${ST_TEST_SCREEN:-Working...}";;
  list-clients) exit 1;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

cat > "$HOME/.local/bin/fzf" <<'EOF'
#!/usr/bin/env bash
cat > "$ST_TEST_PICKER_INPUT"
case ${ST_TEST_PICK_KEY:-escape} in
  escape) exit 130;;
  enter) printf '\n'; sed -n '1p' "$ST_TEST_PICKER_INPUT";;
  delete) printf 'ctrl-d\n'; grep -F 'demo/beta' "$ST_TEST_PICKER_INPUT" | head -1;;
  delete-main) printf 'ctrl-d\n'; grep -F "${ST_TEST_MAIN_SESSION:-demo/master}" "$ST_TEST_PICKER_INPUT" | head -1;;
esac
EOF
chmod +x "$HOME/.local/bin/fzf"

mkdir -p "$TEST_ROOT/demo"
git -C "$TEST_ROOT/demo" init -q
git -C "$TEST_ROOT/demo" -c user.name=Test -c user.email=test@example.com commit -q --allow-empty -m init
git -C "$TEST_ROOT/demo" worktree add -q -b alpha "$ST_WORKTREE_ROOT/demo/alpha"
git -C "$TEST_ROOT/demo" worktree add -q -b beta "$ST_WORKTREE_ROOT/demo/beta"
printf '%s\n' "$TEST_ROOT/demo" > "$ST_STATE/repos"

cd "$TEST_ROOT/demo"
export ST_TEST_MAIN_SESSION
ST_TEST_MAIN_SESSION=$("$ROOT/bin/st" _sessions | grep -x "demo/$(git branch --show-current)")
alpha_session=$("$ROOT/bin/st" _sessions | grep -x 'demo/alpha')
beta_session=$("$ROOT/bin/st" _sessions | grep -x 'demo/beta')
"$ROOT/bin/st" go alpha
"$ROOT/bin/st" go beta
"$ROOT/bin/st" go alpha
[ "$(sed -n '1p' "$ST_STATE/recent")" = "$alpha_session" ] || fail 'recent order did not put alpha first'
[ "$(sed -n '2p' "$ST_STATE/recent")" = "$beta_session" ] || fail 'recent order did not put beta second'

list=$("$ROOT/bin/st" ls)
printf '%s\n' "$list" | grep -Eq '^TREE +SESSION +AGENT +CHANGES +TYPE$' || fail 'list has no grid headings'
printf '%s\n' "$list" | grep -Eq '^demo/alpha +closed +- +clean +worktree$' || fail 'list did not show a readable tree row'
if printf '%s\n' "$list" | grep -Eq '[[:xdigit:]]{32}|/demo-[[:xdigit:]]'; then
  fail 'list exposed internal identity hashes'
fi

: > "$ST_TEST_LOG"
ST_TEST_PICK_KEY=escape "$ROOT/bin/st" go --picker
first=$(sed -n '1p' "$ST_TEST_PICKER_INPUT")
second=$(sed -n '2p' "$ST_TEST_PICKER_INPUT")
case $first in *'demo/alpha'*) ;; *) fail 'picker did not start with alpha';; esac
case $second in *'demo/beta'*) ;; *) fail 'picker did not put beta second';; esac
if grep -Eq '^(attach-session|switch-client) ' "$ST_TEST_LOG"; then
  fail 'Escape switched sessions'
fi

: > "$ST_TEST_LOG"
ST_TEST_PANES=$(printf '%s\tcodex\t%%1\trunning\tcodex\tbash\n%s\tcodex\t%%2\tdone\tcodex\tbash' "$alpha_session" "$beta_session") \
  ST_TEST_SCREEN='Working...' ST_TEST_PICK_KEY=escape "$ROOT/bin/st" go --picker
grep -E 'demo/alpha +running' "$ST_TEST_PICKER_INPUT" >/dev/null || fail 'picker lost running status'
grep -E 'demo/beta +done' "$ST_TEST_PICKER_INPUT" >/dev/null || fail 'picker lost done status'
[ "$(grep -c '^list-panes -a ' "$ST_TEST_LOG")" = 1 ] || fail 'picker did not use one tmux snapshot'
if grep -q '^has-session ' "$ST_TEST_LOG"; then
  fail 'picker queried tmux separately for each tree'
fi

: > "$ST_TEST_LOG"
ST_TEST_PANES=$(printf '%s\tcodex\t%%1\tdone\tcodex\tbash' "$alpha_session") \
  "$ROOT/bin/st" go --popup /dev/ttys999
grep -F 'display-popup -c /dev/ttys999' "$ST_TEST_LOG" >/dev/null || fail 'popup was not opened after preparation'
grep -F 'ST_PICKER_ROWS=' "$ST_TEST_LOG" >/dev/null || fail 'popup did not receive prepared rows'

: > "$ST_TEST_LOG"
ST_PICKER_ROWS=$'· demo/prepared\t/tmp/prepared\tdemo/prepared' \
  ST_TEST_PICK_KEY=escape "$ROOT/bin/st" go --picker
grep -F 'demo/prepared' "$ST_TEST_PICKER_INPUT" >/dev/null || fail 'picker did not use prepared rows'
if grep -q '^list-panes -a ' "$ST_TEST_LOG"; then
  fail 'prepared picker queried tmux again'
fi

ST_TEST_HAS_SESSIONS=1 ST_TEST_AGENT_STATE=running ST_TEST_SCREEN='Working...' \
  "$ROOT/bin/st" status demo/alpha | grep -q $'demo/alpha\trunning' || fail 'running status'
ST_TEST_HAS_SESSIONS=1 ST_TEST_AGENT_STATE=running ST_TEST_SCREEN='› ' \
  "$ROOT/bin/st" status demo/alpha | grep -q $'demo/alpha\tinput' || fail 'input status'
ST_TEST_HAS_SESSIONS=1 ST_TEST_AGENT_STATE=done \
  "$ROOT/bin/st" status demo/alpha | grep -q $'demo/alpha\tdone' || fail 'done status'
"$ROOT/bin/st" status demo/alpha | grep -q $'demo/alpha\tclosed' || fail 'closed status'

: > "$ST_TEST_LOG"
ST_TEST_HAS_SESSIONS=1 ST_TEST_MAIN_CLOSED=1 "$ROOT/bin/st" down --subtrees -y
grep -F "new-session -d -s $ST_TEST_MAIN_SESSION" "$ST_TEST_LOG" >/dev/null || fail 'main fallback was not opened'
grep -F "kill-session -t =$alpha_session" "$ST_TEST_LOG" >/dev/null || fail 'alpha was not closed'
grep -F "kill-session -t =$beta_session" "$ST_TEST_LOG" >/dev/null || fail 'beta was not closed'
if grep -F "kill-session -t =$ST_TEST_MAIN_SESSION" "$ST_TEST_LOG" >/dev/null; then
  fail 'main session was closed'
fi

if ST_TEST_PICK_KEY=delete-main "$ROOT/bin/st" go --picker > "$TEST_ROOT/main.out" 2>&1; then
  fail 'picker allowed deleting the main worktree'
fi
[ -d "$TEST_ROOT/demo" ] || fail 'main worktree disappeared'

printf 'uncommitted\n' > "$ST_WORKTREE_ROOT/demo/beta/new-file"
if printf 'y\n' | ST_TEST_PICK_KEY=delete "$ROOT/bin/st" go --picker > "$TEST_ROOT/dirty.out" 2>&1; then
  fail 'picker deleted a dirty worktree without force'
fi
[ -d "$ST_WORKTREE_ROOT/demo/beta" ] || fail 'dirty worktree disappeared'
rm "$ST_WORKTREE_ROOT/demo/beta/new-file"

printf 'y\n' | ST_TEST_PICK_KEY=delete "$ROOT/bin/st" go --picker
[ ! -d "$ST_WORKTREE_ROOT/demo/beta" ] || fail 'picker did not delete beta'
if grep -Fx "$beta_session" "$ST_STATE/recent" >/dev/null; then
  fail 'deleted tree stayed in recent list'
fi

printf 'ok: recent picker, Escape, agent state, bulk close, and guarded deletion\n'
