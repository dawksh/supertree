#!/usr/bin/env bash
set -euo pipefail
task_repo=$(cd "$(dirname "$0")/.." && pwd)
task_tmp=$(mktemp -d)
trap 'rm -rf "$task_tmp"' EXIT
mkdir -p "$task_tmp/bin"
export SUPERTREE_GUARD_LOG="$task_tmp/log"
export SUPERTREE_SOURCE_ROOT="$task_repo"
cat > "$task_tmp/bin/tmux" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$@" >> "$SUPERTREE_GUARD_LOG"
printf '%s\n' END >> "$SUPERTREE_GUARD_LOG"
FAKE
chmod +x "$task_tmp/bin/tmux"
cat > "$task_tmp/check" <<'CHECK'
#!/usr/bin/env bash
set -euo pipefail
[ "$HOME" = "$SUPERTREE_TEST_SANDBOX/home" ]
[ -z "${TMUX:-}" ] && [ -z "${ST_STATE:-}" ]
tmux kill-server
tmux -S /tmp/tmux-501/default kill-server
tmux -L default kill-server
tmux -N list-sessions
# The application prepends system directories to PATH. Verify it cannot bypass
# the guard; this probe is read-only even if that protection regresses.
mkdir -p "$SUPERTREE_TEST_SANDBOX/source/bin"
cp "$SUPERTREE_SOURCE_ROOT/bin/st" "$SUPERTREE_TEST_SANDBOX/source/bin/st"
cp -R "$SUPERTREE_SOURCE_ROOT/bin/st-lib" "$SUPERTREE_TEST_SANDBOX/source/bin/st-lib"
printf '%s\n' 'sandbox_probe() { tmux -V; }' 'st_register_command sandbox-probe sandbox_probe' > "$SUPERTREE_TEST_SANDBOX/source/bin/st-lib/85-sandbox-probe.sh"
bash "$SUPERTREE_TEST_SANDBOX/source/bin/st" sandbox-probe
CHECK
chmod +x "$task_tmp/check"
PATH="$task_tmp/bin:$PATH" TMUX=/tmp/tmux-501/default,1,1 ST_STATE=/real-user-state \
  bash "$task_repo/scripts/test-local.sh" -- "$task_tmp/check"
awk '
  /^-S$/ { getline target; if (target !~ "^/tmp/st-local-tests[.][^/]+/sockets/[a-f0-9]+[.]sock$") bad=1; count++ }
  target == "/tmp/tmux-501/default" { bad=1 }
  END { exit bad || count != 5 }
' "$SUPERTREE_GUARD_LOG" || { printf 'FAIL: test commands escaped their private socket namespace\n' >&2; exit 1; }
printf 'ok: test runner remaps default, explicit and named tmux sockets and isolates HOME/state\n'
