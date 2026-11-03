#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

installed_home="$TEST_ROOT/installed"
wrong_home="$TEST_ROOT/inherited"
mkdir -p "$installed_home/.local/bin" "$installed_home/.config/supertree" "$wrong_home"
ln -s "$ROOT/bin/st" "$installed_home/.local/bin/st"
printf 'ST_HARNESS=codex\nST_WINDOWS="agent vim shell"\n' > "$installed_home/.config/supertree/config"

output=$(HOME="$wrong_home" "$installed_home/.local/bin/st" doctor 2>&1 || true)
case $output in
  *"harness       Codex (codex)"*) ;;
  *) printf 'FAIL: inherited HOME hid the configured harness\n' >&2; exit 1;;
esac
case $output in
  *"config        $installed_home/.config/supertree/config"*) ;;
  *) printf 'FAIL: inherited HOME selected the wrong config\n' >&2; exit 1;;
esac

stale_config="$TEST_ROOT/deleted-fixture/config"
output=$(HOME="$installed_home" ST_CONFIG="$stale_config" \
  ST_STATE="$TEST_ROOT/deleted-fixture/state" \
  ST_WORKTREE_ROOT="$TEST_ROOT/deleted-fixture/worktrees" \
  "$installed_home/.local/bin/st" doctor 2>&1 || true)
case $output in
  *"harness       Codex (codex)"*"config        $installed_home/.config/supertree/config"*) ;;
  *) printf 'FAIL: stale tmux config overrode installed config\n' >&2; exit 1;;
esac
case $output in
  *"worktree root $installed_home/projects/.worktrees"*) ;;
  *) printf 'FAIL: stale tmux worktree root survived\n' >&2; exit 1;;
esac

printf 'ok: installed command recovers from stale tmux home and config\n'
