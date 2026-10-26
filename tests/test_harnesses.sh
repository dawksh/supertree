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
    '[ "$*" != "${ST_TEST_FAIL_ARGS-none}" ]' \
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

# A failed `claude --continue` falls through instead of starting a new session.
printf 'ST_HARNESS=claude\n' > "$ST_CONFIG"
: > "$ST_TEST_LOG"
ST_STATE="$TEST_ROOT/claude-fail-state" "$ROOT/bin/st" _agent
out=$(ST_TEST_FAIL_ARGS=--continue ST_STATE="$TEST_ROOT/claude-fail-state" \
  "$ROOT/bin/st" _agent 2>&1) || fail 'failed claude --continue made st _agent fail'
assert_log "$(printf '%s\n%s' 'claude|' 'claude|--continue')"
case $out in *"run 'claude' to start fresh"*) ;; *) fail "missing start-fresh hint: [$out]";; esac

# Outside a git repository the pane's harness refuses, writes no markers,
# and still drops into the shell.
outside="$TEST_ROOT/not-a-repo"
mkdir -p "$outside"
printf '%s\n' '#!/bin/sh' 'printf "shell|%s\n" "$*" >> "$ST_TEST_LOG"' > "$TEST_ROOT/shell"
chmod +x "$TEST_ROOT/shell"
: > "$ST_TEST_LOG"
out=$(cd "$outside" && GIT_CEILING_DIRECTORIES="$TEST_ROOT" SHELL="$TEST_ROOT/shell" \
  ST_STATE="$TEST_ROOT/nogit-state" "$ROOT/bin/st" _run _agent 2>&1) ||
  fail 'st _run _agent failed outside a git repository'
case $out in *'not inside a git repository'*) ;; *) fail "unexpected outside-repo output: [$out]";; esac
[ ! -e "$TEST_ROOT/nogit-state/seen" ] || fail 'markers were written outside a git repository'
assert_log 'shell|-i'

if out=$(cd "$outside" && GIT_CEILING_DIRECTORIES="$TEST_ROOT" \
  ST_STATE="$TEST_ROOT/nogit-state" "$ROOT/bin/st" _agent 2>&1); then
  fail 'st _agent succeeded outside a git repository'
fi
case $out in *'not inside a git repository'*) ;; *) fail "direct st _agent outside a repo: [$out]";; esac
[ ! -e "$TEST_ROOT/nogit-state/seen" ] || fail 'direct st _agent wrote markers outside a git repository'

# Session construction names the first window after the configured harness.
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
printf '%s\n' \
  '#!/bin/sh' \
  'printf "%s\n" "$*" >> "$ST_TMUX_LOG"' \
  '[ "${1:-}" = has-session ] && exit 1' \
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

# agent_statuses reads one tmux snapshot and captures only running panes' screens.
screens="$TEST_ROOT/screens"
mkdir -p "$screens"
printf '%s\n' \
  '#!/bin/sh' \
  'printf "%s\n" "$*" >> "$ST_TMUX_LOG"' \
  'case ${1:-} in' \
  '  list-panes) printf "%s\n" "$ST_TEST_PANES";;' \
  '  capture-pane) cat "$ST_TEST_SCREENS/${4#%}" 2>/dev/null || printf "Working...\n";;' \
  'esac' > "$HOME/.local/bin/tmux"
printf '› \n' > "$screens/6"
printf '❯ \n' > "$screens/7"
printf 'Overwrite? [y/N]\n' > "$screens/8"
panes=$(printf '%s\n' \
  $'plain\tclaude\t%1\t\t\tbash\t' \
  $'harness\tclaude\t%2\t\t\tclaude\t' \
  $'node\tclaude\t%3\t\t\tnode\t' \
  $'bun\tclaude\t%4\t\t\tbun\t' \
  $'finished\tclaude\t%5\tdone\t\tclaude\t' \
  $'chevron\tclaude\t%6\trunning\t\tclaude\t' \
  $'arrow\tclaude\t%7\trunning\t\tclaude\t' \
  $'confirm\tclaude\t%8\trunning\t\tclaude\t' \
  $'custom\tclaude\t%9\tdone\tagent\tclaude\t' \
  $'custom\tagent\t%10\trunning\tagent\tnode\t' \
  $'side\tclaude\t%11\t\t\tbash\t1' \
  $'side\tclaude\t%12\trunning\t\tclaude\t' \
  $'nowindow\tshell\t%13\trunning\t\tbash\t')
: > "$ST_TMUX_LOG"
statuses=$(ST_TEST_PANES=$panes ST_TEST_SCREENS=$screens ST_HARNESS=claude ST_HARNESS_COMMAND='' \
  PATH="$HOME/.local/bin:$PATH" bash -c 'set -euo pipefail
    st_register_command() { :; }
    . "$1/bin/st-lib/00-core.sh"; . "$1/bin/st-lib/40-agent.sh"
    agent_statuses' _ "$ROOT") || fail 'agent_statuses failed'
expected=$(printf '%s\t%s\n' plain done harness running node running bun running finished done \
  chevron input arrow input confirm input custom running side running nowindow done)
[ "$statuses" = "$expected" ] || fail "agent_statuses gave [$statuses], expected [$expected]"
[ "$(grep -c '^list-panes -a ' "$ST_TMUX_LOG")" = 1 ] || fail 'agent_statuses did not use one list-panes -a call'
[ "$(grep -c '^list-panes' "$ST_TMUX_LOG")" = 1 ] || fail 'agent_statuses listed panes per session'
captured=$(sed -n 's/^capture-pane -p -t \(%[0-9]*\) .*/\1/p' "$ST_TMUX_LOG" | tr '\n' ' ')
[ "$captured" = '%2 %3 %4 %6 %7 %8 %10 %12 ' ] || fail "screens captured for non-running panes: [$captured]"

printf 'ok: harness launch, continuation, tmux window selection, and agent statuses\n'
