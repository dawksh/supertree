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
  display-message)
    case ${last:-} in
      '#{client_tty}') printf '%s\n' "$ST_TEST_TTY";;
      '#S') cat "$ST_TEST_CURRENT";;
      '#{window_id}')
        session=$(cat "$ST_TEST_CURRENT")
        case $session in origin) window=@1;; tree-a) window=@2;; *) window=@3;; esac
        printf '%s\n' "$window"
        ;;
    esac
    ;;
  has-session)
    grep -qxF -- "${target%%:*}" "$ST_TEST_LIVE"
    ;;
  switch-client)
    session=${target%%:*}
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

printf '%s\n' origin tree-a tree-b > "$ST_TEST_LIVE"
printf 'origin\n' > "$ST_TEST_CURRENT"
origin_file="$ST_STATE/origin/-dev-ttys001"

go() {
  local label=$1 session=$2
  ST_PICKER_ROWS=$(printf '%s  closed\t%s\t%s' "$label" "$repo" "$session") \
    "$ROOT/bin/st" go "$label"
}

go demo/a tree-a
go demo/b tree-b
[ "$(cat "$ST_TEST_CURRENT")" = tree-b ] || fail 'nested switch did not reach tree B'
expected=$(printf 'origin\t@1\ntree-a\t@2')
[ "$(cat "$origin_file")" = "$expected" ] || fail 'nested origins were not pushed in order'

"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = tree-a ] || fail 'first leave did not return to tree A'
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
printf 'tree-b\n' > "$ST_TEST_CURRENT"
printf 'origin\t@7\nstale-session\t@99\ntree-b\t@8\n' > "$origin_file"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'leave did not skip duplicate and stale origins'
[ ! -s "$origin_file" ] || fail 'stale origins remained after unwind'
[ ! -e "$ST_TEST_DETACHED" ] || fail 'stale history caused a detach despite a live origin'

# A legacy single-session file remains readable.
printf 'tree-b\n' > "$ST_TEST_CURRENT"
printf 'origin\n' > "$origin_file"
"$ROOT/bin/st" leave "$ST_TEST_TTY"
[ "$(cat "$ST_TEST_CURRENT")" = origin ] || fail 'legacy origin record was not restored'
[ ! -s "$origin_file" ] || fail 'legacy origin record was not consumed'

# If a recorded window disappeared, restoration falls back to the session.
printf 'tree-b\n' > "$ST_TEST_CURRENT"
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
