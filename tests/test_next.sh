#!/usr/bin/env bash
set -euo pipefail

task_repo=$(cd "$(dirname "$0")/.." && pwd)
task_tmp=$(mktemp -d)
trap 'rm -rf "$task_tmp"' EXIT
mkdir -p "$task_tmp/bin" "$task_tmp/user"
cp "$task_repo/bin/st" "$task_tmp/bin/st"
cp -R "$task_repo/bin/st-lib" "$task_tmp/bin/st-lib"
ln -s "$task_tmp/bin/st" "$task_tmp/linked-st"
export ST_CONFIG="$task_tmp/config"
export ST_STATE="$task_tmp/initial-state"
export ST_WORKTREE_ROOT="$task_tmp/initial-trees"
: > "$ST_CONFIG"

if "$task_tmp/linked-st" next inspect --json >"$task_tmp/out" 2>"$task_tmp/error"; then
  printf 'FAIL: missing Go client was accepted\n' >&2
  exit 1
fi
grep -q 'go build -o bin/st-next ./cmd/st-next' "$task_tmp/error"
[ ! -s "$task_tmp/out" ]

# Global config is resolved by the shell and forwarded without splitting args.
printf 'ST_STATE=%q\nST_WORKTREE_ROOT=%q\n' "$task_tmp/config state" "$task_tmp/config trees" > "$ST_CONFIG"
cat > "$task_tmp/bin/st-next" <<'CLIENT'
#!/usr/bin/env bash
printf '%s\n' "$ST_STATE" "$ST_WORKTREE_ROOT" "$#" "$@"
exit 7
CLIENT
chmod +x "$task_tmp/bin/st-next"
status=0
"$task_tmp/linked-st" next invoke workspace.inspect '{"workspace_id":"with spaces"}' > "$task_tmp/out" || status=$?
[ "$status" = 7 ]
expected=$(printf '%s\n' "$task_tmp/config state" "$task_tmp/config trees" 3 invoke workspace.inspect '{"workspace_id":"with spaces"}')
[ "$(cat "$task_tmp/out")" = "$expected" ]

printf 'ok: next binary discovery, symlink, config forwarding, arguments, and exit status\n'
