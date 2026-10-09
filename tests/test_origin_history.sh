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
export ST_TEST_LIVE="$TEST_ROOT/live"
export ST_TEST_CURRENT="$TEST_ROOT/current"
export ST_TEST_DETACHED="$TEST_ROOT/detached"
export ST_TEST_TTY=/dev/ttys001
export TMUX=fake
export TMUX_PANE=%1
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_WINDOWS=shell\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ST_TEST_LOG"

target=''
while [ $# -gt 0 ]; do
  case $1 in
    -t) target=${2#=}; shift 2;;
    -c) shift 2;;
    *) command=${command:-$1}; last=$1; shift;;
  esac
done

case ${command:-} in
  list-sessions)
    if [[ ${last:-} == *'#{session_id}'* ]]; then
      printf '$1\torigin\n$2\t%s\n$3\t%s\n' "$ST_TEST_TREE_A" "$ST_TEST_TREE_B"
    else
      printf 'origin\n%s\n%s\n' "$ST_TEST_TREE_A" "$ST_TEST_TREE_B"
    fi
    ;;
  display-message)
    case ${last:-} in
      '#{client_tty}') printf '%s\n' "$ST_TEST_TTY";;
      '#S') cat "$ST_TEST_CURRENT";;
      '#{window_id}')
        session=$(cat "$ST_TEST_CURRENT")
        case $session in origin) window=@1;; "$ST_TEST_TREE_A") window=@2;; *) window=@3;; esac
        printf '%s\n' "$window"
        ;;
    esac
    ;;
  show-options)
    option=${last:-}
    if [ "$option" = @supertree_label ]; then
      case $target in
        '$2'|"$ST_TEST_TREE_A") printf 'repo/branch-a\n';;
        '$3'|"$ST_TEST_TREE_B") printf 'repo/branch-b\n';;
      esac
    fi
    ;;
  has-session)
    grep -qxF -- "${target%%:*}" "$ST_TEST_LIVE"
    ;;
  switch-client)
    session=${target%%:*}
    case $session in
      '$1') session=origin;;
      '$2') session=$ST_TEST_TREE_A;;
      '$3') session=$ST_TEST_TREE_B;;
    esac
    grep -qxF -- "$session" "$ST_TEST_LIVE" || exit 1
    if [ -f "$ST_TEST_FAIL_SWITCH" ] && grep -qxF -- "$target" "$ST_TEST_FAIL_SWITCH"; then
      : > "$ST_TEST_FAIL_SWITCH"
      exit 1
    fi
    printf '%s\n' "$session" > "$ST_TEST_CURRENT"
    ;;
  detach-client)
    : > "$ST_TEST_DETACHED"
    ;;
  set-option) ;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

repo="$TEST_ROOT/repo"
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m init
git -C "$repo" worktree add -q -b branch-a "$ST_WORKTREE_ROOT/repo/branch-a"
git -C "$repo" worktree add -q -b branch-b "$ST_WORKTREE_ROOT/repo/branch-b"
printf '%s\n' "$repo" > "$ST_STATE/repos"
export ST_TEST_TREE_A=$($ROOT/bin/st _sessions | grep '/branch-a$')
export ST_TEST_TREE_B=$($ROOT/bin/st _sessions | grep '/branch-b$')

printf '%s\n' origin "$ST_TEST_TREE_A" "$ST_TEST_TREE_B" > "$ST_TEST_LIVE"
printf 'origin\n' > "$ST_TEST_CURRENT"
origin_file="$ST_STATE/origin/-dev-ttys001"

go() {
  local label=$1 path=$2 session=$3
  ST_PICKER_ROWS=$(printf '%s  closed\t%s\t%s' "$label" "$path" "$session") \
    "$ROOT/bin/st" go "$label"
}

go repo/branch-a "$ST_WORKTREE_ROOT/repo/branch-a" "$ST_TEST_TREE_A"
go repo/branch-b "$ST_WORKTREE_ROOT/repo/branch-b" "$ST_TEST_TREE_B"
[ "$(cat "$ST_TEST_CURRENT")" = "$ST_TEST_TREE_B" ] || fail 'nested switch did not reach tree B'
expected=$(printf 'origin\t@1\n%s\t@2' "$ST_TEST_TREE_A")
[ "$(cat "$origin_file")" = "$expected" ] || fail 'nested origins were not pushed in order'

"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = "$ST_TEST_TREE_A" ] || fail 'first leave did not return to tree A'
[ "$(cat "$origin_file")" = $'origin\t@1' ] || fail 'first leave did not consume tree A'

"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'second leave did not return to the original session'
[ ! -s "$origin_file" ] || fail 'second leave did not consume the origin history'
[ ! -e "$ST_TEST_DETACHED" ] || fail 'nested unwind detached a live client'

"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ -e "$ST_TEST_DETACHED" ] || fail 'empty origin history did not detach'

# Missing entries and entries equal to the current session are consumed rather
# than blocking an older live origin.
rm -f "$ST_TEST_DETACHED"
printf '%s\n' "$ST_TEST_TREE_B" > "$ST_TEST_CURRENT"
printf 'origin\t@7\nstale-session\t@99\n%s\t@8\n' "$ST_TEST_TREE_B" > "$origin_file"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'leave did not skip duplicate and stale origins'
[ ! -s "$origin_file" ] || fail 'stale origins remained after unwind'
[ ! -e "$ST_TEST_DETACHED" ] || fail 'stale history caused a detach despite a live origin'

# A legacy single-session file remains readable.
printf '%s\n' "$ST_TEST_TREE_B" > "$ST_TEST_CURRENT"
printf 'origin\n' > "$origin_file"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'legacy origin record was not restored'
[ ! -s "$origin_file" ] || fail 'legacy origin record was not consumed'

# If a recorded window disappeared, restoration falls back to the session.
printf '%s\n' "$ST_TEST_TREE_B" > "$ST_TEST_CURRENT"
printf 'origin\t@missing\n' > "$origin_file"
export ST_TEST_FAIL_SWITCH="$TEST_ROOT/fail-switch"
printf 'origin:@missing\n' > "$ST_TEST_FAIL_SWITCH"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'missing origin window did not fall back to its session'
grep -F 'switch-client -c /dev/ttys001 -t =origin:@missing' "$ST_TEST_LOG" >/dev/null ||
  fail 'recorded origin window was not attempted'
grep -F 'switch-client -c /dev/ttys001 -t =origin' "$ST_TEST_LOG" >/dev/null ||
  fail 'origin session fallback was not attempted'

printf 'ok: nested origin history, stale entries, legacy records, and window fallback\n'
