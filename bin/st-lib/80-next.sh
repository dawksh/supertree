# shellcheck shell=bash
# Opt-in Go workspace commands. The existing shell remains the entrypoint.

cmd_next() {
  local binary=$st_bin_dir/st-next
  [ -x "$binary" ] || die "st next is not built; from the source checkout run: go build -o bin/st-next ./cmd/st-next"
  # Forward resolved legacy configuration without having Go source shell code.
  export ST_STATE ST_WORKTREE_ROOT
  exec "$binary" "$@"
}

st_register_command next cmd_next 'inspect the experimental workspace core' 'next <inspect|commands|invoke> [args]'
