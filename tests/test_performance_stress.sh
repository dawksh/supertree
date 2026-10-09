#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
TEST_ROOT=$(cd "$TEST_ROOT" && pwd -P)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_CONFIG="$TEST_ROOT/config"
export ST_WORKTREE_ROOT="$TEST_ROOT/worktrees with space"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_WINDOWS=shell\n' > "$ST_CONFIG"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# Inventory and list behavior do not require a live server. Keeping this fake
# empty also makes accidental per-tree tmux probing visible in failures.
cat > "$HOME/.local/bin/tmux" <<'EOF'
#!/usr/bin/env bash
case ${1:-} in
  list-sessions) exit 0;;
  has-session) exit 1;;
  *) exit 0;;
esac
EOF
chmod +x "$HOME/.local/bin/tmux"

repo="$TEST_ROOT/repository with space/stress repo"
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" -c user.name=Test -c user.email=test@example.invalid \
  commit -q --allow-empty -m init

branches=(
  alpha
  nested/one
  nested-one
  dot.name
  dot_name
  percent%x
  hash#x
  'semi;x'
  "quote'x"
  plus+x
)

for branch in "${branches[@]}"; do
  path=$(cd "$repo" && "$ROOT/bin/st" new "$branch" --bare)
  [ -d "$path" ] || fail "new --bare did not create $branch"
done

sessions=$(cd "$repo" && "$ROOT/bin/st" _sessions)
expected=$((${#branches[@]} + 1))
[ "$(printf '%s\n' "$sessions" | wc -l | tr -d ' ')" = "$expected" ] ||
  fail 'inventory lost a main checkout or linked worktree'
[ -z "$(printf '%s\n' "$sessions" | sort | uniq -d)" ] ||
  fail 'colliding branch labels produced duplicate session names'

printf 'dirty\n' > "$(git -C "$repo" worktree list --porcelain |
  awk '/^worktree /{p=substr($0,10)} /^branch refs\/heads\/nested-one$/{print p}')/dirty.txt"
listing=$(cd "$repo" && "$ROOT/bin/st" ls)
main_branch=$(git -C "$repo" branch --show-current)
printf '%s\n' "$listing" | grep -Eq "^stress-repo/$main_branch +closed +- +clean +main$" ||
  fail 'space-containing main path was misclassified'
printf '%s\n' "$listing" | grep -Eq '^stress-repo/nested-one +closed +- +modified +worktree$' ||
  fail 'dirty linked worktree status was lost'

printf 'ok: fast inventory across collisions, metacharacters, and spaced paths\n'
