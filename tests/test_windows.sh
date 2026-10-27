#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
export ST_TEST_SESSION='supertree/main'
export ST_TEST_PATH="$ROOT"
mkdir -p "$HOME/.local/bin" "$ST_STATE"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_log_contains() {
  grep -F -- "$1" "$ST_TMUX_LOG" >/dev/null || fail "tmux log did not contain: $1"
}

assert_log_excludes() {
  if grep -F -- "$1" "$ST_TMUX_LOG" >/dev/null; then
    fail "tmux log unexpectedly contained: $1"
  fi
}

# Stub tmux. ST_TEST_LIVE lists open sessions; ST_TEST_PLAIN_SESSIONS lists
# sessions without a supertree label; ST_TEST_RACE_FLAG makes new-session lose
# a race to a concurrent creator.
cat > "$HOME/.local/bin/tmux" <<'STUB'
#!/usr/bin/env bash
printf "%s\n" "$*" >> "$ST_TMUX_LOG"
target='' prev=''
for arg in "$@"; do [ "$prev" = -t ] && target=$arg; prev=$arg; done
target=${target#=}
target=${target%%:*}
is_live() { case " ${ST_TEST_LIVE:-} " in *" $1 "*) return 0;; esac; return 1; }
case ${1:-} in
  has-session)
    [ -n "${ST_TEST_RACE_FLAG:-}" ] && [ -f "$ST_TEST_RACE_FLAG" ] && exit 0
    is_live "$target" && exit 0
    exit 1;;
  new-session)
    if [ -n "${ST_TEST_RACE_FLAG:-}" ]; then : > "$ST_TEST_RACE_FLAG"; exit 1; fi;;
  list-sessions)
    for s in ${ST_TEST_LIVE:-}; do printf "%s\n" "$s"; done;;
  show-options)
    case " ${ST_TEST_PLAIN_SESSIONS:-} " in
      *" $target "*) ;;
      *) printf "%s\n" "${ST_TEST_LABEL-supertree/main}";;
    esac;;
  display-message)
    last=${!#}
    case $last in
      "#W") printf "%s\n" "${ST_TEST_CURRENT_WINDOW:-codex}";;
      "#S") printf "%s\n" "$ST_TEST_SESSION";;
      "#{pane_current_path}") printf "%s\n" "$ST_TEST_PATH";;
      "#{client_tty}") printf "%s\n" "${ST_TEST_TTY:-}";;
    esac;;
esac
exit 0
STUB
chmod +x "$HOME/.local/bin/tmux"

fixture="$TEST_ROOT/supertree"
git init -q "$fixture"
git -C "$fixture" checkout -qb main
git -C "$fixture" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m init
# A main checkout is only listed while it has a worktree under the root.
mkdir -p "$HOME/projects/.worktrees"
git -C "$fixture" worktree add -q -b feature \
  "$(cd "$HOME/projects/.worktrees" && pwd -P)/supertree/feature"
printf '%s\n' "$fixture" > "$ST_STATE/repos"
main_session=$("$ROOT/bin/st" _sessions | grep -x 'supertree/main' | head -1)
[ -n "$main_session" ] || fail 'main session was not listed'
export ST_TEST_SESSION="$main_session"

run_layout() {
  local windows=${1:-__default__}
  if [ "$windows" = __default__ ]; then
    printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
  else
    printf 'ST_HARNESS=codex\nST_WINDOWS=%q\n' "$windows" > "$ST_CONFIG"
  fi
  : > "$ST_TMUX_LOG"
  "$ROOT/bin/st" go main
}

# Existing configs keep the original three-window layout.
run_layout
assert_log_contains '-n codex'
assert_log_contains '-n vim'
assert_log_contains '-n shell'
assert_log_contains "select-window -t =$main_session:codex"

# External-editor users can omit vim entirely.
run_layout 'agent shell'
assert_log_contains '-n codex'
assert_log_contains '-n shell'
assert_log_excludes '-n vim'
[ "$(grep -cE '^(new-session|new-window) ' "$ST_TMUX_LOG")" = 2 ] ||
  fail 'agent shell did not create exactly two windows'

# Configured order controls creation and startup focus.
run_layout 'shell agent'
first_create=$(grep -E '^(new-session|new-window) ' "$ST_TMUX_LOG" | head -1)
case $first_create in *'-n shell'*) ;; *) fail 'shell was not the first created window';; esac
assert_log_contains "select-window -t =$main_session:shell"

# Numbered selection and toggle use configured positions.
printf 'ST_HARNESS=codex\nST_WINDOWS=%q\n' 'agent shell' > "$ST_CONFIG"
: > "$ST_TMUX_LOG"
"$ROOT/bin/st" window 2
assert_log_contains "select-window -t =$main_session:shell"

: > "$ST_TMUX_LOG"
ST_TEST_CURRENT_WINDOW=codex "$ROOT/bin/st" toggle
assert_log_contains "select-window -t =$main_session:shell"

# Disabled tools are not reported as dependencies.
printf 'ST_HARNESS=codex\nST_WINDOWS=shell\n' > "$ST_CONFIG"
doctor_output=$("$ROOT/bin/st" doctor 2>&1 || true)
case $doctor_output in *'  ok    nvim'*|*'  MISS  nvim'*) fail 'doctor checked disabled vim window';; esac
case $doctor_output in *'  ok    codex'*|*'  MISS  codex'*) fail 'doctor checked disabled agent window';; esac

for invalid in 'agent nope' 'agent agent' ''; do
  printf 'ST_HARNESS=codex\nST_WINDOWS=%q\n' "$invalid" > "$ST_CONFIG"
  if "$ROOT/bin/st" doctor >"$TEST_ROOT/invalid.out" 2>&1; then
    fail "invalid window list unexpectedly succeeded: [$invalid]"
  fi
done

# Set-option targets use the exact `=sess:` form.
run_layout
assert_log_contains "set-option -t =$main_session: @supertree_label"
assert_log_contains "set-option -t =$main_session: @st_agent_window"
assert_log_excludes "set-option -t $main_session "

# The launch command quotes the st path, so a path with a space still runs.
mkdir -p "$TEST_ROOT/with space"
ln -s "$ROOT/bin/st" "$TEST_ROOT/with space/st"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
: > "$ST_TMUX_LOG"
PATH="$TEST_ROOT/with space:$PATH" "$ROOT/bin/st" go main
assert_log_contains "$(printf '%q' "$TEST_ROOT/with space/st") _run _agent"
assert_log_contains "$(printf '%q' "$TEST_ROOT/with space/st") _run nvim"

# Commands tmux runs through run-shell or a hook expand #{} formats, so # is doubled there only.
odd_dir="$TEST_ROOT/we ird'\$HOME#S"
mkdir -p "$odd_dir"
ln -s "$ROOT/bin/st" "$odd_dir/st"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
: > "$ST_TMUX_LOG"
PATH="$odd_dir:$PATH" "$ROOT/bin/st" go main
assert_log_contains "$(printf '%q' "$odd_dir/st") _run _agent"
assert_log_contains "after-new-window run-shell \"'$TEST_ROOT/we ird'\\\\''\\\$HOME##S/st' _sidebar-attach '#{window_id}'\""
assert_log_contains "run-shell -b '$TEST_ROOT/we ird'\\''\$HOME##S/st' _sidebar-poll >/dev/null 2>&1"
rm "$odd_dir/st"

# A real, isolated tmux server runs those commands, with a stub st that logs its arguments.
real_tmux=$(command -v tmux || true)
if [ -n "$real_tmux" ]; then
  socket="st-windows-test-$$"
  trap 'TMUX_TMPDIR="$TEST_ROOT" "$real_tmux" -L "$socket" kill-server >/dev/null 2>&1 || true; rm -rf "$TEST_ROOT"' EXIT
  e2e_log="$TEST_ROOT/e2e.log"
  mkdir -p "$TEST_ROOT/e2e/.local/bin"
  printf '%s\n' '#!/bin/sh' 'printf "%s|" "$@" >> "$ST_E2E_LOG"; echo >> "$ST_E2E_LOG"' > "$odd_dir/st"
  printf '#!/bin/sh\nexec %q -L %q -f /dev/null "$@"\n' "$real_tmux" "$socket" > "$TEST_ROOT/e2e/.local/bin/tmux"
  chmod +x "$odd_dir/st" "$TEST_ROOT/e2e/.local/bin/tmux"
  : > "$e2e_log"
  e2e() {
    HOME="$TEST_ROOT/e2e" PATH="$odd_dir:$PATH" ST_E2E_LOG="$e2e_log" TMUX_TMPDIR="$TEST_ROOT" \
      ST_WORKTREE_ROOT="$TEST_ROOT/home/projects/.worktrees" "$@"
  }
  e2e_tmux() { e2e "$real_tmux" -L "$socket" -f /dev/null "$@"; }
  # An optional key is resent each try: fzf drops keys that arrive before it has drawn.
  wait_e2e() {
    for _ in $(seq 50); do
      [ -z "${2:-}" ] || e2e_tmux send-keys -t "$sidebar_pane" "$2"
      grep -Eqx -- "$1" "$e2e_log" && return 0
      sleep 0.1
    done
    fail "isolated tmux never ran st with: $1 (log: $(tr '\n' ' ' < "$e2e_log")) (st output: $(tr '\n' ' ' < "$TEST_ROOT/e2e.out" 2>/dev/null))"
  }
  printf 'ST_HARNESS=codex\nST_WINDOWS=shell\n' > "$ST_CONFIG"
  e2e_tmux new-session -d -s probe 'sleep 60'
  e2e "$ROOT/bin/st" go main > "$TEST_ROOT/e2e.out" 2>&1 || true
  wait_e2e '_sidebar-poll\|'
  e2e_tmux new-window -d -t "=$main_session" >/dev/null 2>&1 || fail 'the after-new-window hook failed'
  wait_e2e '_sidebar-attach\|@[0-9]+\|'
  TMUX_PANE=$(e2e_tmux display-message -p -t '=probe:' '#{pane_id}') e2e "$ROOT/bin/st" _sidebar-open +new
  wait_e2e 'go\|--popup\|\|'

  # The sidebar's fzf runs its key bindings through the shell, not tmux: no doubled #.
  if command -v fzf >/dev/null; then
    sidebar_pane=$(e2e_tmux new-window -d -t '=probe' -P -F '#{pane_id}' "$ROOT/bin/st" _sidebar)
    wait_e2e '_sidebar-open\|.*' Enter
    wait_e2e '_sidebar-rows\|' C-r
  fi
  e2e_tmux kill-server >/dev/null 2>&1 || true
  rm -rf "$ST_STATE/sidebar"
fi

# Window shortcuts refuse outside a supertree session and create nothing.
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
for args in 'window 1' 'toggle' 'agent'; do
  : > "$ST_TMUX_LOG"
  # shellcheck disable=SC2086
  out=$(ST_TEST_LABEL='' "$ROOT/bin/st" $args 2>&1) || fail "st $args failed outside a tree session"
  case $out in *'not a supertree session'*) ;; *) fail "st $args did not refuse: [$out]";; esac
  assert_log_excludes 'new-window'
  assert_log_excludes 'select-window'
done

# A concurrent `st go` that creates the session first counts as success.
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
: > "$ST_TMUX_LOG"
ST_TEST_RACE_FLAG="$TEST_ROOT/race" "$ROOT/bin/st" go main ||
  fail 'losing the session-creation race was treated as failure'
assert_log_contains "set-option -t =$main_session: @supertree_label"
assert_log_contains "attach-session -t =$main_session"
assert_log_excludes 'new-window'
rm -f "$TEST_ROOT/race"

# The origin is recorded only from a non-tree session; tree hops keep it.
feature_session=$("$ROOT/bin/st" _sessions | grep -x 'supertree/feature' | head -1)
[ -n "$feature_session" ] || fail 'feature session was not listed'
tty=/dev/ttys042
origin_file="$ST_STATE/origin/$(printf '%s' "$tty" | tr '/' '-')"
rm -rf "$ST_STATE/origin"
TMUX=fake ST_TEST_TTY=$tty ST_TEST_SESSION=work ST_TEST_PLAIN_SESSIONS=work \
  "$ROOT/bin/st" go feature
[ "$(cat "$origin_file" 2>/dev/null)" = work ] || fail 'origin was not recorded from a non-tree session'
TMUX=fake ST_TEST_TTY=$tty ST_TEST_SESSION="$feature_session" "$ROOT/bin/st" go main
[ "$(cat "$origin_file")" = work ] || fail 'hopping between trees overwrote the origin'
rm -rf "$ST_STATE/origin"
TMUX=fake ST_TEST_TTY=$tty ST_TEST_SESSION="$feature_session" "$ROOT/bin/st" go main
[ ! -e "$origin_file" ] || fail 'a tree session was recorded as the origin'

# Leaving returns to the origin and forgets it.
mkdir -p "$ST_STATE/origin"
printf 'work\n' > "$origin_file"
: > "$ST_TMUX_LOG"
TMUX=fake ST_TEST_LIVE=work ST_TEST_SESSION="$main_session" "$ROOT/bin/st" leave "$tty"
assert_log_contains "switch-client -c $tty -t =work"
[ ! -e "$origin_file" ] || fail 'st leave kept the origin file'

# Modules share one namespace; a reused helper name silently replaces the other.
duplicates=$(cat "$ROOT"/bin/st-lib/*.sh |
  sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)() *{.*/\1/p' | sort | uniq -d)
[ -z "$duplicates" ] || fail "helpers defined more than once: $duplicates"

# `st down <name>` closes the matching tree, by label or by partial name.
for name in supertree/feature feature; do
  : > "$ST_TMUX_LOG"
  ST_TEST_LIVE="$main_session $feature_session" "$ROOT/bin/st" down "$name" ||
    fail "st down $name failed"
  assert_log_contains "kill-session -t =$feature_session"
  assert_log_excludes "kill-session -t =$main_session"
done
: > "$ST_TMUX_LOG"
out=$("$ROOT/bin/st" down feature 2>&1) || fail 'st down on a closed tree failed'
case $out in *'supertree/feature already closed'*) ;; *) fail "unexpected down output: [$out]";; esac
assert_log_excludes 'kill-session'

# --all/--subtrees still close open trees when the last known tree is closed.
git -C "$fixture" worktree add -q -b zlast \
  "$(cd "$HOME/projects/.worktrees" && pwd -P)/supertree/zlast"
last_session=$("$ROOT/bin/st" _sessions | tail -1)
[ "$last_session" = 'supertree/zlast' ] || fail "unexpected last tree: [$last_session]"

: > "$ST_TMUX_LOG"
ST_TEST_LIVE="$main_session" "$ROOT/bin/st" down --all -y 2>/dev/null ||
  fail 'st down --all failed'
assert_log_contains "kill-session -t =$main_session"
assert_log_excludes "kill-session -t =$last_session"

: > "$ST_TMUX_LOG"
ST_TEST_LIVE="$feature_session" "$ROOT/bin/st" down --subtrees -y 2>/dev/null ||
  fail 'st down --subtrees failed'
assert_log_contains "kill-session -t =$feature_session"
assert_log_contains "new-session -d -s $main_session"
assert_log_excludes "kill-session -t =$main_session"
assert_log_excludes "kill-session -t =$last_session"

printf 'ok: window layouts, shortcuts, origins, races, and st down\n'
