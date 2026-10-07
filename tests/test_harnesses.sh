#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_TEST_LOG="$TEST_ROOT/agent.log"
mkdir -p "$HOME/.local/bin"

for command in claude codex opencode aider; do
  printf '%s\n' \
    '#!/bin/sh' \
    'printf "%s|%s\n" "$(basename "$0")" "$*" >> "$ST_TEST_LOG"' \
    > "$HOME/.local/bin/$command"
  chmod +x "$HOME/.local/bin/$command"
done

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_log() {
  local expected=$1 actual
  actual=$(cat "$ST_TEST_LOG")
  [ "$actual" = "$expected" ] || fail "expected [$expected], got [$actual]"
}

run_builtin() {
  local harness=$1 expected_start=$2 expected_resume=$3
  printf 'ST_HARNESS=%s\n' "$harness" > "$ST_CONFIG"
  : > "$ST_TEST_LOG"
  "$ROOT/bin/st" _agent
  "$ROOT/bin/st" _agent
  assert_log "$(printf '%s\n%s' "$expected_start" "$expected_resume")"
}

run_builtin claude 'claude|' 'claude|--continue'
run_builtin codex 'codex|' 'codex|resume --last'
run_builtin openrouter 'opencode|' 'opencode|--continue'

printf '%s\n' \
  'ST_HARNESS=aider' \
  "ST_HARNESS_COMMAND='aider --new'" \
  "ST_HARNESS_RESUME_COMMAND='aider --resume'" > "$ST_CONFIG"
: > "$ST_TEST_LOG"
"$ROOT/bin/st" _agent
"$ROOT/bin/st" _agent
assert_log "$(printf '%s\n%s' 'aider|--new' 'aider|--resume')"

# Environment choice overrides the user config.
printf 'ST_HARNESS=claude\n' > "$ST_CONFIG"
: > "$ST_TEST_LOG"
ST_HARNESS=codex ST_STATE="$TEST_ROOT/override-state" "$ROOT/bin/st" _agent
assert_log 'codex|'

# Session construction names the first window after the configured harness.
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
export ST_TMUX_OPTIONS="$TEST_ROOT/tmux-options"
export ST_TMUX_SESSIONS="$TEST_ROOT/tmux-sessions"
: > "$ST_TMUX_SESSIONS"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "%s\n" "$*" >> "$ST_TMUX_LOG"' \
  'case ${1:-} in' \
  '  has-session) exit 1;;' \
  '  new-session)' \
  '    args=("$@"); for ((i=1; i<${#args[@]}; i++)); do' \
  '      [ "${args[i]}" = -s ] && printf "%s\n" "${args[i+1]}" >> "$ST_TMUX_SESSIONS"' \
  '    done;;' \
  '  list-sessions)' \
  '    awk '\''!seen[$0]++ { print "$" NR "\t" $0 }'\'' "$ST_TMUX_SESSIONS";;' \
  '  set-option)' \
  '    args=("$@"); for ((i=1; i<${#args[@]}; i++)); do' \
  '      [ "${args[i]}" = -t ] && sess=${args[i+1]#=}' \
  '      [[ ${args[i]} = @* ]] && { opt=${args[i]}; value=${args[i+1]}; }' \
  '    done; printf "%s|%s|%s\n" "$sess" "$opt" "$value" >> "$ST_TMUX_OPTIONS";;' \
  '  show-options)' \
  '    args=("$@"); for ((i=1; i<${#args[@]}; i++)); do' \
  '      [ "${args[i]}" = -t ] && sess=${args[i+1]#=}' \
  '      [[ ${args[i]} = @* ]] && opt=${args[i]}' \
  '    done; awk -F "[|]" -v s="$sess" -v o="$opt" '\''$1 == s && $2 == o { value=$3 } END { if (value != "") print value }'\'' "$ST_TMUX_OPTIONS" 2>/dev/null;;' \
  'esac' \
  'exit 0' > "$HOME/.local/bin/tmux"
chmod +x "$HOME/.local/bin/tmux"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
tmux_state="$TEST_ROOT/tmux-state"
mkdir -p "$tmux_state"
fixture="$TEST_ROOT/supertree"
git init -q "$fixture"
git -C "$fixture" -c user.name=Test -c user.email=test@example.com \
  commit -q --allow-empty -m init
git -C "$fixture" worktree add -q -b multi-agent "$TEST_ROOT/multi-agent"
printf '%s\n' "$fixture" > "$tmux_state/repos"
ST_STATE="$tmux_state" ST_WORKTREE_ROOT="$TEST_ROOT" "$ROOT/bin/st" go multi-agent
grep -F -- '-n codex' "$ST_TMUX_LOG" >/dev/null || fail 'session did not create a codex window'
grep -F -- '_run _agent' "$ST_TMUX_LOG" >/dev/null || fail 'session did not use the generic agent launcher'

printf 'ok: harness launch, continuation, and tmux window selection\n'
