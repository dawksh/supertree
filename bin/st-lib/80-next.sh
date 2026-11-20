# shellcheck shell=bash
# Opt-in Go workspace commands. The existing shell remains the entrypoint.

cmd_next() {
  local binary=$st_bin_dir/st-next
  if [ "$ST_VERSION" != dev ]; then
    binary=$st_lib_dir/st-next
    [ -x "$binary" ] || die "st next is not installed; rerun this release's installer with ST_WITH_NEXT=1 (or ST_WITH_NEXT=1 st repair)"
  else
    [ -x "$binary" ] || die "st next is not built; from the source checkout run: go build -o bin/st-next ./cmd/st-next"
  fi
  # Forward resolved legacy configuration without having Go source shell code.
  export ST_STATE ST_WORKTREE_ROOT
  exec "$binary" "$@"
}

st_register_command next cmd_next 'use the experimental workspace core' 'next <inspect|commands|invoke|daemon|events> [args]'
