#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

# ui_run invokes these functions through an error-handling conditional. A setup
# failure must stop the hook before it can run from the caller's directory.
source "$ROOT/bin/st-lib/05-ui.sh"
source "$ROOT/bin/st-lib/30-bootstrap.sh"
UI_ROWS=0
marker="$TEST_ROOT/hook-ran"
marker_write() { : > "$marker"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

trusted_config_snapshot=''
if ( bootstrap_step install run_install "$TEST_ROOT/missing" marker_write ) > "$TEST_ROOT/install.out" 2>&1; then
  fail 'install reported success after cd failed'
fi
[ ! -e "$marker" ] || fail 'install ran in the caller directory after cd failed'

trusted_config_snapshot="$TEST_ROOT/missing-config"
if ( bootstrap_step hook run_post_create "$TEST_ROOT" feature 1 marker_write ) > "$TEST_ROOT/hook.out" 2>&1; then
  fail 'post-create reported success after config loading failed'
fi
[ ! -e "$marker" ] || fail 'post-create hook ran after config loading failed'

printf 'ok: bootstrap setup failures stop install and post-create hooks\n'
