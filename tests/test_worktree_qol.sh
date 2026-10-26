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

main_branch=$(git -C "$TEST_ROOT/demo" branch --show-current)
: > "$ST_TEST_LOG"
list=$(ST_TEST_SCREEN='Working...' ST_TEST_PANES=$(printf '%s\tcodex\t%%1\trunning\tcodex\tbash\n%s\tcodex\t%%2\tdone\tcodex\tbash\n%s\tcodex\t%%3\tdone\tcodex\tcodex' \
  "$alpha_session" "$beta_session" "$ST_TEST_MAIN_SESSION") "$ROOT/bin/st" ls)
printf '%s\n' "$list" | grep -Eq "^demo/$main_branch +open +done +clean +main\$" || fail 'list did not mark the main checkout'
printf '%s\n' "$list" | grep -Eq '^demo/alpha +open +running +clean +worktree$' || fail 'list lost running agent'
printf '%s\n' "$list" | grep -Eq '^demo/beta +open +done +clean +worktree$' || fail 'list lost done agent'
[ "$(grep -c '^list-panes -a ' "$ST_TEST_LOG")" = 1 ] || fail 'list did not use one tmux snapshot'
if grep -q '^has-session ' "$ST_TEST_LOG"; then fail 'list queried tmux separately for each tree'; fi
ST_TEST_SCREEN='› ' ST_TEST_PANES=$(printf '%s\tcodex\t%%1\trunning\tcodex\tbash' "$alpha_session") \
  "$ROOT/bin/st" ls | grep -Eq '^demo/alpha +open +input +clean +worktree$' || fail 'list lost input status'
# Empty @st_agent_state and @st_agent_window leave consecutive tabs in the snapshot.
list=$(ST_TEST_SCREEN='Working...' ST_TEST_PANES=$(printf '%s\tcodex\t%%1\t\t\tcodex\n%s\tcodex\t%%2\t\t\tbash' \
  "$alpha_session" "$beta_session") "$ROOT/bin/st" ls)
printf '%s\n' "$list" | grep -Eq '^demo/alpha +open +running +clean +worktree$' || fail 'list lost untracked running agent'
printf '%s\n' "$list" | grep -Eq '^demo/beta +open +done +clean +worktree$' || fail 'list showed an untracked shell as running'

: > "$ST_TEST_LOG"
ST_TEST_PICK_KEY=escape "$ROOT/bin/st" go --picker
first=$(sed -n '2p' "$ST_TEST_PICKER_INPUT")
second=$(sed -n '3p' "$ST_TEST_PICKER_INPUT")
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
"$ROOT/bin/st" status "demo/$main_branch" | grep -qx "demo/$main_branch"$'\tclosed' || fail 'status did not resolve main tree'
"$ROOT/bin/st" status "$beta_session" | grep -qx $'demo/beta\tclosed' || fail 'status did not resolve by session name'
if "$ROOT/bin/st" status demo/nope > "$TEST_ROOT/status.out" 2>&1; then fail 'status accepted an unknown tree'; fi
grep -q 'no tree matching: demo/nope' "$TEST_ROOT/status.out" || fail 'unknown tree status message changed'

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

if "$ROOT/bin/st" new "$main_branch" > "$TEST_ROOT/new-main.out" 2>&1; then
  fail 'new accepted the main checkout branch'
fi
grep -F "$main_branch is the main checkout" "$TEST_ROOT/new-main.out" | grep -qF "use 'st go'" ||
  fail 'new did not point the main branch at st go'

for bad_base in --detach no-such-ref; do
  if "$ROOT/bin/st" new zeta --from "$bad_base" --bare > "$TEST_ROOT/bad-base.out" 2>&1; then
    fail "new accepted base $bad_base"
  fi
  grep -Eq "(invalid|unknown) base: $bad_base" "$TEST_ROOT/bad-base.out" || fail "new did not reject base $bad_base"
  if git -C "$TEST_ROOT/demo" show-ref --verify --quiet refs/heads/zeta; then fail "bad base $bad_base created a branch"; fi
  if git -C "$TEST_ROOT/demo" worktree list | grep -q '/zeta '; then fail "bad base $bad_base created a worktree"; fi
done

git -C "$TEST_ROOT/demo" branch epsilon
epsilon_dir=$("$ROOT/bin/st" new epsilon --from x --bare 2> "$TEST_ROOT/from.err")
grep -qF 'epsilon already exists; ignoring --from x' "$TEST_ROOT/from.err" || fail 'new did not warn about ignored --from'
[ -d "$epsilon_dir" ] || fail 'new did not check out the existing branch'
git -C "$TEST_ROOT/demo" worktree remove "$epsilon_dir"

gamma_dir=$("$ROOT/bin/st" new gamma --bare 2>/dev/null)
# Folder names carry a 5-char hash; state keys keep the full one.
repo=$(cd "$ST_STATE/idx" && ls -d demo-*)
gamma_slug=$(cd "$ST_STATE/idx/$repo" && ls -d gamma-*)
epsilon_slug=$(cd "$ST_STATE/idx/$repo" && ls -d epsilon-*)
gamma_session=$("$ROOT/bin/st" _sessions | grep '/gamma$')
mkdir -p "$ST_STATE/seen/$repo"
touch "$ST_STATE/seen/$repo/$gamma_slug.customharness" "$ST_STATE/seen/$repo/$gamma_slug.codex" \
  "$ST_STATE/seen/$repo/$epsilon_slug.customharness"
git -C "$TEST_ROOT/demo" worktree lock "$gamma_dir"
: > "$ST_TEST_LOG"
if printf 'y\n' | "$ROOT/bin/st" rm gamma > "$TEST_ROOT/locked.out" 2>&1; then
  fail 'rm reported success on a locked worktree'
fi
[ -d "$gamma_dir" ] || fail 'locked worktree disappeared'
if grep -q '^kill-session ' "$ST_TEST_LOG"; then fail 'rm closed the session although removal failed'; fi
[ -f "$ST_STATE/seen/$repo/$gamma_slug.customharness" ] || fail 'failed rm cleared tree state'

git -C "$TEST_ROOT/demo" worktree unlock "$gamma_dir"
: > "$ST_TEST_LOG"
printf 'y\n' | "$ROOT/bin/st" rm gamma > /dev/null 2>&1 || fail 'rm failed after unlock'
[ ! -d "$gamma_dir" ] || fail 'rm left the worktree'
grep -qxF "kill-session -t =$gamma_session" "$ST_TEST_LOG" || fail 'rm did not close the session'
[ ! -e "$ST_STATE/seen/$repo/$gamma_slug.customharness" ] || fail 'rm left a custom harness marker'
[ ! -e "$ST_STATE/seen/$repo/$gamma_slug.codex" ] || fail 'rm left a codex marker'
[ -f "$ST_STATE/seen/$repo/$epsilon_slug.customharness" ] || fail 'rm cleared another tree marker'

git init -q --bare "$TEST_ROOT/origin.git"
git -C "$TEST_ROOT/demo" remote add origin "$TEST_ROOT/origin.git"
git -C "$TEST_ROOT/demo" push -q origin "$main_branch"
delta_dir=$("$ROOT/bin/st" new delta --bare 2>/dev/null)
git -C "$delta_dir" -c user.name=Test -c user.email=test@example.com commit -q --allow-empty -m 'local only'
printf 'x\n' > "$delta_dir/dirty-file"
if printf 'y\n' | "$ROOT/bin/st" rm delta > "$TEST_ROOT/report.out" 2>&1; then
  fail 'rm removed a dirty unpushed tree'
fi
expected=$(printf 'uncommitted changes:\n?? dirty-file\nunpushed commits:\nlocal only')
[ "$(head -4 "$TEST_ROOT/report.out")" = "$expected" ] || fail 'rm dirty report changed'
grep -qF 'refusing to remove delta — commit/push, or pass --force' "$TEST_ROOT/report.out" || fail 'rm refusal message changed'
[ -d "$delta_dir" ] || fail 'refused rm removed the tree'

printf 'ok: recent picker, Escape, agent state, bulk close, and guarded deletion\n'
