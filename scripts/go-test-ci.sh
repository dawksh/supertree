#!/usr/bin/env bash
# Keep the command's JSON log in the step log; publish only sanitized names.
set -uo pipefail
log=$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/st-go-test.XXXXXX") || exit 1
trap 'rm -f -- "$log"' EXIT
"$@" | tee "$log"
statuses=("${PIPESTATUS[@]}")
test_status=${statuses[0]}
tee_status=${statuses[1]}
summary_status=0
python3 scripts/go-test-annotations.py "$test_status" < "$log" || summary_status=$?
if [ "$test_status" -ne 0 ]; then exit "$test_status"; fi
if [ "$tee_status" -ne 0 ]; then exit "$tee_status"; fi
exit "$summary_status"
