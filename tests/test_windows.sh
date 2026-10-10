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

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "%s\n" "$*" >> "$ST_TMUX_LOG"' \
  'case ${1:-} in' \
  '  has-session) exit 1;;' \
  '  display-message)' \
  '    last=${!#}' \
  '    case $last in' \
  '      "#W") printf "%s\n" "${ST_TEST_CURRENT_WINDOW:-codex}";;' \
  '      "#S") printf "%s\n" "$ST_TEST_SESSION";;' \
  '      "#{pane_current_path}") printf "%s\n" "$ST_TEST_PATH";;' \
  '    esac;;' \
  'esac' \
  'exit 0' > "$HOME/.local/bin/tmux"
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

printf 'ok: configurable window layouts, shortcuts, and dependencies\n'
