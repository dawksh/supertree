#!/usr/bin/env bash
set -euo pipefail

task_repo=$(cd "$(dirname "$0")/.." && pwd)
task_base=$(mktemp -d)
task_tmp="$task_base/checkout with #'quote"
trap 'rm -rf "$task_base"' EXIT
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

# Native attachment stays an explicit, argument-preserving Go action and never
# redirects through a tmux popup, including paths with shell/format characters.
status=0
"$task_tmp/linked-st" native attach --state "$task_tmp/native state #quote" --terminal term_123 --generation 1 --read-only > "$task_tmp/out" || status=$?
[ "$status" = 7 ]
expected=$(printf '%s\n' "$task_tmp/config state" "$task_tmp/config trees" 9 native attach --state "$task_tmp/native state #quote" --terminal term_123 --generation 1 --read-only)
[ "$(cat "$task_tmp/out")" = "$expected" ]

# Outside tmux, the optional controls use the same client and exact arguments.
status=0
TMUX= "$task_tmp/linked-st" control --workspace 'with spaces' > "$task_tmp/out" || status=$?
[ "$status" = 7 ]
expected=$(printf '%s\n' "$task_tmp/config state" "$task_tmp/config trees" 3 tui --workspace 'with spaces')
[ "$(cat "$task_tmp/out")" = "$expected" ]

# Inside tmux, only open a popup. Preserve context and shell/format quoting,
# even for checkout paths containing spaces, quotes and tmux's # character.
export HOME="$task_tmp/user" ST_TEST_POPUP_ARGS="$task_tmp/popup-args"
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/tmux" <<'TMUX'
#!/usr/bin/env bash
printf '%s\0' "$@" > "$ST_TEST_POPUP_ARGS"
TMUX
chmod +x "$HOME/.local/bin/tmux"
TMUX=/tmp/fixture,123,0 "$task_tmp/linked-st" control --workspace 'with spaces'
python3 - "$task_tmp" <<'PY'
import pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1])
args = (root / 'popup-args').read_bytes().decode().split('\0')[:-1]
assert args[0] == 'display-popup', args
assert '-EE' in args and args[args.index('-d') + 1] == str(pathlib.Path.cwd())
# Simulate tmux's format unescaping before running the fake Go client.
result = subprocess.run(['/bin/bash', '-c', args[-1].replace('##', '#')], capture_output=True, text=True)
assert result.returncode == 7, result.stderr
assert result.stdout.splitlines() == [str(root / 'config state'), str(root / 'config trees'), '3', 'tui', '--workspace', 'with spaces']
PY

printf 'ok: next binary discovery, optional control popup, quoting, config forwarding, arguments, and exit status\n'
