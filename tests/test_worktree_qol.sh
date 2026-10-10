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
export ST_TMUX_OPTIONS="$TEST_ROOT/tmux-options"
export ST_TMUX_SESSIONS="$TEST_ROOT/tmux-sessions"
export ST_TEST_PICKER_INPUT="$TEST_ROOT/picker.txt"
export ST_TEST_HASH_LOG="$TEST_ROOT/hash.log"
export ST_TEST_REAL_HASH
ST_TEST_REAL_HASH=$(command -v shasum || command -v sha256sum)
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
: > "$ST_TMUX_SESSIONS"
: > "$ST_TMUX_OPTIONS"
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
  set-option)
    case $* in *'-t =demo-'*) exit 1;; esac
    args=("$@"); for ((i=1; i<${#args[@]}; i++)); do
      [ "${args[i]}" = -t ] && sess=${args[i+1]#=}
      [[ ${args[i]} = @* ]] && { opt=${args[i]}; value=${args[i+1]}; }
    done
    case $sess in '$'*) sess=${sess#\$};; esac
    printf '%s|%s|%s\n' "$sess" "$opt" "$value" >> "$ST_TMUX_OPTIONS";;
  show-options)
    case $* in
      *'@st_agent_window'*) printf 'codex\n';;
      *'@st_agent_state'*) printf '%s\n' "${ST_TEST_AGENT_STATE:-running}";;
      *)
        args=("$@"); for ((i=1; i<${#args[@]}; i++)); do
          [ "${args[i]}" = -t ] && sess=${args[i+1]#=}
          [[ ${args[i]} = @* ]] && opt=${args[i]}
        done
        case $sess in '$'*) sess=${sess#\$};; esac
        awk -F '[|]' -v s="$sess" -v o="$opt" \
          '$1 == s && $2 == o { value=$3 } END { if (value != "") print value }' \
          "$ST_TMUX_OPTIONS" 2>/dev/null;;
    esac;;
  list-sessions)
    case $* in
      *session_id*) ;;
      *) [ "${ST_TEST_HAS_SESSIONS:-0}" = 1 ] || exit 0;;
    esac
    ids=0
    metadata=0
    case $* in *session_id*) ids=1;; esac
    case $* in *supertree_identity*) metadata=1;; esac
    awk -F '[|]' \
      -v ids="$ids" \
      -v metadata="$metadata" '
      FILENAME == ARGV[1] && $2 == "@supertree_identity" { id[$1]=$3; next }
      FILENAME == ARGV[1] && $2 == "@supertree_label" { label[$1]=$3; next }
      FILENAME == ARGV[1] { next }
      !seen[$0]++ {
        if (ids && metadata) print "$" $0 "\t" $0 "\t" id[$0] "\t" label[$0]
        else print ids ? "$" $0 "\t" $0 : (metadata ? $0 "\t" id[$0] "\t" label[$0] : $0)
      }
    ' "$ST_TMUX_OPTIONS" "$ST_TMUX_SESSIONS" 2>/dev/null;;
  new-session)
    args=("$@"); for ((i=1; i<${#args[@]}; i++)); do
      [ "${args[i]}" = -s ] && printf '%s\n' "${args[i+1]}" >> "$ST_TMUX_SESSIONS"
    done
    true;;
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

cat > "$HOME/.local/bin/shasum" <<'EOF'
#!/usr/bin/env bash
printf 'hash\n' >> "$ST_TEST_HASH_LOG"
case $ST_TEST_REAL_HASH in
  */shasum) exec "$ST_TEST_REAL_HASH" "$@";;
  *) [ "${1:-}" != -a ] || shift 2
     exec "$ST_TEST_REAL_HASH" "$@";;
esac
EOF
chmod +x "$HOME/.local/bin/shasum"

mkdir -p "$TEST_ROOT/demo"
git -C "$TEST_ROOT/demo" init -q
git -C "$TEST_ROOT/demo" -c user.name=Test -c user.email=test@example.com commit -q --allow-empty -m init
git -C "$TEST_ROOT/demo" worktree add -q -b alpha "$ST_WORKTREE_ROOT/demo/alpha"
git -C "$TEST_ROOT/demo" worktree add -q -b beta "$ST_WORKTREE_ROOT/demo/beta"
printf '%s\n' "$TEST_ROOT/demo" > "$ST_STATE/repos"

cd "$TEST_ROOT/demo"
# With no legacy sessions, discovery hashes the repository identity once and
# must not hash every branch merely to rule legacy names out.
: > "$ST_TEST_HASH_LOG"
"$ROOT/bin/st" _sessions >/dev/null
hash_calls=$(wc -l < "$ST_TEST_HASH_LOG" | tr -d ' ')
[ "$hash_calls" = 1 ] || fail "tree discovery performed $hash_calls hashes without legacy sessions"

export ST_TEST_MAIN_SESSION
ST_TEST_MAIN_SESSION=$("$ROOT/bin/st" _sessions | grep -x "demo/$(git branch --show-current)")
alpha_session=$("$ROOT/bin/st" _sessions | grep -x 'demo/alpha')
beta_session=$("$ROOT/bin/st" _sessions | grep -x 'demo/beta')
"$ROOT/bin/st" go alpha
"$ROOT/bin/st" go beta
"$ROOT/bin/st" go alpha
[ "$(sed -n '1p' "$ST_STATE/recent")" = "$alpha_session" ] || fail 'recent order did not put alpha first'
[ "$(sed -n '2p' "$ST_STATE/recent")" = "$beta_session" ] || fail 'recent order did not put beta second'

# Direct queries select by inventory only; interactive pane/status data is not
# displayed and should not delay the switch.
: > "$ST_TEST_LOG"
"$ROOT/bin/st" go alpha
if grep -q '^list-panes -a ' "$ST_TEST_LOG"; then
  fail 'direct query collected interactive picker pane state'
fi

assert_query_session() {
  local query=$1 rows=$2 expected=$3 actual
  : > "$ST_TEST_LOG"
  ST_PICKER_ROWS=$rows "$ROOT/bin/st" go "$query"
  actual=$(awk '$1 == "attach-session" { print $3 }' "$ST_TEST_LOG" | tail -1)
  [ "$actual" = "\$$expected" ] ||
    fail "query '$query' selected ${actual#\$} instead of $expected"
}

query_rows=$(printf '%s\t%s\t%s\n' \
  'demo/feature/a-b  closed' '/tmp/feature-a-b' 'demo/feature/a-b' \
  'demo/feature-a-b  closed' "$TEST_ROOT/demo" 'demo/feature-a-b')
assert_query_session feature-a-b "$query_rows" demo/feature-a-b

query_rows=$(printf '%s\t%s\t%s\n' \
  'demo/dot.name  closed' "$TEST_ROOT/demo" 'sess-dot' \
  'demo/dot_name  closed' "$TEST_ROOT/demo" 'sess-underscore')
assert_query_session dot_name "$query_rows" sess-underscore

query_rows=$(printf '%s\t%s\t%s\n' \
  'demo/regexaxb  closed' "$TEST_ROOT/demo" 'sess-regex-decoy' \
  'demo/regexa.b  closed' "$TEST_ROOT/demo" 'sess-regex-literal')
assert_query_session regexa.b "$query_rows" sess-regex-literal

query_rows=$(printf '%s\t%s\t%s\n' \
  'demo/ends  closed' "$TEST_ROOT/demo" 'sess-end-decoy' \
  'demo/ends$  closed' "$TEST_ROOT/demo" 'sess-end-literal')
assert_query_session 'ends$' "$query_rows" sess-end-literal

query_rows=$(printf '%s\t%s\t%s\n' \
  'demo/unrelated  closed' '/tmp/path-with-needle' 'sess-hidden-decoy' \
  'demo/visible-needle  closed' "$TEST_ROOT/demo" 'sess-visible-match')
assert_query_session NEEDLE "$query_rows" sess-visible-match

query_rows=$(printf '%s\t%s\t%s\n' \
  'other/demo/topic  closed' "$TEST_ROOT/demo" 'sess-branch-exact' \
  'demo/topic  closed' "$TEST_ROOT/demo" 'sess-label-exact')
assert_query_session demo/topic "$query_rows" sess-label-exact

list=$("$ROOT/bin/st" ls)
printf '%s\n' "$list" | grep -Eq '^TREE +SESSION +AGENT +CHANGES +TYPE$' || fail 'list has no grid headings'
printf '%s\n' "$list" | grep -Eq '^demo/alpha +closed +- +clean +worktree$' || fail 'list did not show a readable tree row'
main_branch=$(git -C "$TEST_ROOT/demo" branch --show-current)
printf '%s\n' "$list" | grep -Eq "^demo/$main_branch +closed +- +clean +main$" || fail 'list did not identify the main checkout'
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
  ST_TEST_HAS_SESSIONS=1 ST_TEST_SCREEN='Working...' ST_TEST_PICK_KEY=escape "$ROOT/bin/st" go --picker
grep -E 'demo/alpha +running' "$ST_TEST_PICKER_INPUT" >/dev/null || fail 'picker lost running status'
grep -E 'demo/beta +done' "$ST_TEST_PICKER_INPUT" >/dev/null || fail 'picker lost done status'
[ "$(grep -c '^list-panes -a ' "$ST_TEST_LOG")" = 1 ] || fail 'picker did not use one tmux snapshot'
session_scans=$(grep -c '^list-sessions ' "$ST_TEST_LOG")
[ "$session_scans" = 2 ] || fail "picker fetched tmux sessions $session_scans times"
if grep -q '^has-session ' "$ST_TEST_LOG"; then
  fail 'picker queried tmux separately for each tree'
fi

: > "$ST_TEST_LOG"
ST_TEST_PANES=$(printf '%s\tcodex\t%%1\tdone\tcodex\tbash' "$alpha_session") \
  ST_TEST_HAS_SESSIONS=1 "$ROOT/bin/st" go --popup /dev/ttys999
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
grep -F "kill-session -t \$$alpha_session" "$ST_TEST_LOG" >/dev/null || fail 'alpha was not closed'
grep -F "kill-session -t \$$beta_session" "$ST_TEST_LOG" >/dev/null || fail 'beta was not closed'
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
