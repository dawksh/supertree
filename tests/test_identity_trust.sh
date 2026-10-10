#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_WORKTREE_ROOT="$TEST_ROOT/trees"
export ST_CONFIG="$TEST_ROOT/missing-config"
export ST_WINDOWS=shell
mkdir -p "$HOME/.local/bin" "$TEST_ROOT/a/demo" "$TEST_ROOT/b/demo"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

for repo in "$TEST_ROOT/a/demo" "$TEST_ROOT/b/demo"; do
  git -C "$repo" init -q
  git -C "$repo" -c user.name=Reviewer -c user.email=reviewer@example.invalid \
    commit -q --allow-empty -m init
done

printf '%s\n' '#!/bin/sh' '[ "$1" = has-session ] && exit 1' 'exit 0' \
  > "$HOME/.local/bin/tmux"
chmod +x "$HOME/.local/bin/tmux"

new_bare() { ( cd "$1" && "$ROOT/bin/st" new "$2" --bare | tail -1 ); }

slash=$(new_bare "$TEST_ROOT/a/demo" feature/a-b)
if ( cd "$TEST_ROOT/a/demo" && printf 'y\n' | "$ROOT/bin/st" rm feature-a-b ) >/dev/null 2>&1; then
  fail 'rm accepted a branch with only a colliding slug'
fi
[ -d "$slash" ] || fail 'rm removed the wrong worktree'

dash=$(new_bare "$TEST_ROOT/a/demo" feature-a-b)
[ "$slash" != "$dash" ] || fail 'colliding branch names share a path'
export ST_TEST_AGENT_LOG="$TEST_ROOT/agent.log"
printf '%s\n' '#!/bin/sh' 'printf "%s|%s\n" "$PWD" "$*" >> "$ST_TEST_AGENT_LOG"' \
  > "$HOME/.local/bin/codex"
chmod +x "$HOME/.local/bin/codex"
( cd "$slash" && ST_HARNESS=codex "$ROOT/bin/st" _agent )
( cd "$dash" && ST_HARNESS=codex "$ROOT/bin/st" _agent )
[ "$(wc -l < "$ST_TEST_AGENT_LOG" | tr -d ' ')" = 2 ] || fail 'agent did not launch in both trees'
if grep -q 'resume' "$ST_TEST_AGENT_LOG"; then
  fail 'one colliding branch reused the other branch agent marker'
fi
other=$(new_bare "$TEST_ROOT/b/demo" feature/a-b)
[ "$slash" != "$other" ] || fail 'same-named repositories share a path'

sessions=$("$ROOT/bin/st" _sessions)
[ "$(printf '%s\n' "$sessions" | sort -u | wc -l | tr -d ' ')" = \
  "$(printf '%s\n' "$sessions" | wc -l | tr -d ' ')" ] || fail 'session identities collided'

( cd "$TEST_ROOT/a/demo" && printf 'y\n' | "$ROOT/bin/st" rm feature-a-b ) >/dev/null
[ -d "$slash" ] || fail 'rm removed the colliding branch worktree'
[ ! -d "$dash" ] || fail 'rm did not remove the selected worktree'

# A valid tree from the old path layout remains usable.
legacy="$ST_WORKTREE_ROOT/demo/legacy-x"
git -C "$TEST_ROOT/a/demo" worktree add -q -b legacy/x "$legacy"
mkdir -p "$ST_STATE/idx/demo"
printf '7\n' > "$ST_STATE/idx/demo/legacy-x"
legacy_reused=$(new_bare "$TEST_ROOT/a/demo" legacy/x)
[ "$legacy_reused" -ef "$legacy" ] || fail 'a valid legacy worktree was not reused'
legacy_index=$(find "$ST_STATE/idx" -type f -name 'legacy-x-*' -exec cat {} \;)
[ "$legacy_index" = 7 ] || fail 'the legacy tree index changed'

config="$TEST_ROOT/a/demo/.supertree"
printf '%s\n' "ST_POST_CREATE='touch \"\$ST_TREE_DIR/trusted-ran\"'" > "$config"
if ( cd "$TEST_ROOT/a/demo" && "$ROOT/bin/st" new untrusted ) >/dev/null 2>&1; then
  fail 'untrusted repository config executed'
fi
untrusted_tree=$(git -C "$TEST_ROOT/a/demo" worktree list --porcelain |
  awk '/^branch refs\/heads\/untrusted$/{print}')
[ -z "$untrusted_tree" ] || fail 'untrusted config left a worktree behind'

( cd "$TEST_ROOT/a/demo" && "$ROOT/bin/st" trust ) >/dev/null
( cd "$TEST_ROOT/a/demo" && "$ROOT/bin/st" new trusted ) >/dev/null
trusted_dir=$(git -C "$TEST_ROOT/a/demo" worktree list --porcelain |
  awk '/^worktree /{p=substr($0,10)} /^branch refs\/heads\/trusted$/{print p}')
[ -f "$trusted_dir/trusted-ran" ] || fail 'trusted config did not run'

printf '%s\n' "ST_POST_CREATE='touch \"\$ST_TREE_DIR/changed-ran\"'" > "$config"
if ( cd "$TEST_ROOT/a/demo" && "$ROOT/bin/st" new changed ) >/dev/null 2>&1; then
  fail 'changed repository config executed without renewed trust'
fi

printf 'ok: unique worktree identity, safe removal, and config trust\n'
