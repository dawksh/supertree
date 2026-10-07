#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
REAL_TMUX=$(command -v tmux || true)
[ -n "$REAL_TMUX" ] || { printf 'skip: tmux is not installed\n'; exit 0; }
SOCKET="st-ownership-$$"
cleanup() {
  "$REAL_TMUX" -L "$SOCKET" kill-server 2>/dev/null || true
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees"
export ST_REAL_TMUX="$REAL_TMUX"
export ST_TEST_SOCKET="$SOCKET"
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
export PATH="$HOME/.local/bin:$PATH"
printf 'ST_WINDOWS=shell\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# Keep tmux state real while making attach/switch non-interactive.
cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
case ${1:-} in
  attach-session|switch-client)
    printf '%s\n' "$*" >> "$ST_TMUX_LOG"
    exit 0;;
esac
exec "$ST_REAL_TMUX" -L "$ST_TEST_SOCKET" "$@"
EOF
chmod +x "$HOME/.local/bin/tmux"

repo="$TEST_ROOT/demo"
git init -q "$repo"
git -C "$repo" -c user.name=Test -c user.email=test@example.invalid \
  commit -q --allow-empty -m init
branch=$(git -C "$repo" branch --show-current)
git -C "$repo" worktree add -q -b linked "$ST_WORKTREE_ROOT/demo/linked"
printf '%s\n' "$repo" > "$ST_STATE/repos"
session="demo/$branch"

# A foreign session with the calculated readable name is neither adopted nor
# killed, even though a known tree has that name.
tmux new-session -d -s "$session" -c "$repo" -n foreign
if "$ROOT/bin/st" go "$branch" >"$TEST_ROOT/foreign-go.out" 2>&1; then
  fail 'go adopted a foreign same-name session'
fi
grep -q 'not owned by supertree' "$TEST_ROOT/foreign-go.out" ||
  fail 'go did not explain the ownership collision'
tmux has-session -t "=$session" 2>/dev/null || fail 'go killed the foreign session'
[ -z "$(tmux show-options -qv -t "$session" @supertree_identity 2>/dev/null || true)" ] ||
  fail 'go stamped ownership onto the foreign session'

if "$ROOT/bin/st" down "$branch" >"$TEST_ROOT/foreign-down.out" 2>&1; then
  fail 'down accepted a foreign same-name session'
fi
grep -q 'refusing to close it' "$TEST_ROOT/foreign-down.out" ||
  fail 'down did not explain the ownership collision'
tmux has-session -t "=$session" 2>/dev/null || fail 'down killed the foreign session'
tmux kill-session -t "=$session"

# Pre-marker Supertree sessions are migrated only when their exact historical
# label matches the tree. A conflicting durable identity is never overwritten.
tmux new-session -d -s "$session" -c "$repo" -n shell
tmux set-option -q -t "$session" @supertree_label "demo/$branch"
tmux set-option -q -t "$session" @supertree_identity 'v1:wrong-tree'
if "$ROOT/bin/st" go "$branch" >"$TEST_ROOT/wrong-id.out" 2>&1; then
  fail 'go accepted a session carrying another tree identity'
fi
wrong_identity=$(tmux show-options -qv -t "$session" @supertree_identity)
[ "$wrong_identity" = 'v1:wrong-tree' ] ||
  fail "go overwrote a conflicting durable identity: [$wrong_identity]"
tmux kill-session -t "=$session"

# The old hashed-name adoption path applies the same rule before renaming.
repo_id=$(printf '%s' "$repo" | shasum -a 256 | awk '{print substr($1,1,32)}')
branch_id=$(printf '%s' "$branch" | shasum -a 256 | awk '{print substr($1,1,32)}')
hashed_session="demo-$repo_id/$branch-$branch_id"
tmux new-session -d -s "$hashed_session" -c "$repo" -n foreign
"$ROOT/bin/st" _sessions >/dev/null
tmux has-session -t "=$hashed_session" 2>/dev/null || fail 'foreign hashed session was renamed'
if tmux has-session -t "=$session" 2>/dev/null; then fail 'foreign hashed session claimed the readable name'; fi
tmux kill-session -t "=$hashed_session"

tmux new-session -d -s "$hashed_session" -c "$repo" -n shell
tmux set-option -q -t "$hashed_session" @supertree_label "demo/$branch"
"$ROOT/bin/st" _sessions | grep -Fx "$session" >/dev/null ||
  fail 'legacy hashed session was not adopted under its readable name'
if tmux has-session -t "=$hashed_session" 2>/dev/null; then fail 'legacy hashed name remained after adoption'; fi
tmux has-session -t "=$session" 2>/dev/null || fail 'adopted legacy session is missing'
"$ROOT/bin/st" go "$branch"
identity=$(tmux show-options -qv -t "$session" @supertree_identity)
case $identity in v1:????????????????????????????????) ;; *) fail 'legacy session was not migrated';; esac
grep -E 'attach-session -t \$[0-9]+$' "$ST_TMUX_LOG" >/dev/null ||
  fail 'migrated legacy session was not attached by validated ID'
"$ROOT/bin/st" down "$branch" >/dev/null
if tmux has-session -t "=$session" 2>/dev/null; then fail 'owned legacy session was not closed'; fi

# Fresh sessions receive the identity immediately and remain manageable.
: > "$ST_TMUX_LOG"
"$ROOT/bin/st" go "$branch"
fresh_identity=$(tmux show-options -qv -t "$session" @supertree_identity)
case $fresh_identity in v1:????????????????????????????????) ;; *) fail 'fresh session has no ownership identity';; esac
"$ROOT/bin/st" down "$branch" >/dev/null
if tmux has-session -t "=$session" 2>/dev/null; then fail 'fresh owned session was not closed'; fi

printf 'ok: foreign session isolation, legacy migration, and durable ownership\n'
