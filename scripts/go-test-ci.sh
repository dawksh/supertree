#!/usr/bin/env bash
# Keep the command's JSON log in the step log; publish only sanitized names.
set -uo pipefail
mode=test
if [ "${1:-}" = --vet ]; then mode=vet; shift; fi
log_dir=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/st-go-test.XXXXXX") || exit 1
log="$log_dir/events"
trap 'rm -rf -- "$log_dir"' EXIT
# Prefix stdout and stderr independently, including CR-delimited lines, so raw
# compiler or test output cannot issue workflow commands. Save raw JSON only
# in a private temporary file for the annotation summarizer.
# Bash 3.2 cannot reliably wait for an exec process substitution. Use a normal
# background child and private FIFO so its exit status remains observable.
mkfifo "$log_dir/stderr" || exit 1
python3 scripts/go-test-log.py < "$log_dir/stderr" >&2 &
stderr_pid=$!
exec 3> "$log_dir/stderr"
"$@" 2>&3 | tee "$log" | python3 scripts/go-test-log.py
statuses=("${PIPESTATUS[@]}")
exec 3>&-
stderr_status=0
wait "$stderr_pid" || stderr_status=$?
test_status=${statuses[0]}
tee_status=${statuses[1]}
log_status=${statuses[2]}
summary_status=0
if [ "$mode" = vet ]; then
  if [ "$test_status" -ne 0 ]; then printf '%s\n' '::error::Go vet failed'; fi
else
  python3 scripts/go-test-annotations.py "$test_status" < "$log" || summary_status=$?
fi
if [ "$test_status" -ne 0 ]; then exit "$test_status"; fi
if [ "$tee_status" -ne 0 ]; then exit "$tee_status"; fi
if [ "$log_status" -ne 0 ]; then exit "$log_status"; fi
if [ "$stderr_status" -ne 0 ]; then exit "$stderr_status"; fi
exit "$summary_status"
