# shellcheck shell=bash
# Opt-in Go workspace commands. The existing shell remains the entrypoint.

next_binary() {
  local binary=$st_bin_dir/st-next
  if [ "$ST_VERSION" != dev ]; then
    binary=$st_lib_dir/st-next
    [ -x "$binary" ] || die "st next is not installed; rerun this release's installer with ST_WITH_NEXT=1 (or ST_WITH_NEXT=1 st repair)"
  else
    [ -x "$binary" ] || die "st next is not built; from the source checkout run: go build -o bin/st-next ./cmd/st-next"
  fi
  printf '%s' "$binary"
}

cmd_next() {
  local binary
  binary=$(next_binary) || return
  # Forward resolved legacy configuration without having Go source shell code.
  export ST_STATE ST_WORKTREE_ROOT ST_THEME ST_ASCII
  exec "$binary" "$@"
}

cmd_control() {
  local binary launch key word
  binary=$(next_binary) || return
  if [ -z "${TMUX:-}" ]; then cmd_next tui "$@"; return; fi
  # Keep the existing tree/editor/shell in place. The control surface is optional
  # and closes back to the terminal which opened it.
  # A long-lived server can carry stale routing/configuration. Clear those
  # values before applying the invoking terminal's resolved environment.
  launch='env -u ST_STATE -u ST_WORKTREE_ROOT -u ST_CONFIG -u ST_CONFDIR -u ST_DEV_ROOT -u ST_NEXT_SOCKET -u ST_NEXT_TMUX_SOCKET -u CODEX_HOME -u ST_THEME -u ST_ASCII -u NO_COLOR'
  for key in HOME PATH ST_STATE ST_WORKTREE_ROOT ST_CONFIG ST_CONFDIR ST_DEV_ROOT ST_NEXT_SOCKET ST_NEXT_TMUX_SOCKET CODEX_HOME ST_THEME ST_ASCII NO_COLOR; do
    if [ "${!key+x}" = x ]; then
      printf -v word '%q' "$key=${!key}"
      launch="$launch $word"
    fi
  done
  printf -v word '%q' "$binary"
  launch="$launch $word tui"
  for key in "$@"; do
    printf -v word '%q' "$key"
    launch="$launch $word"
  done
  # tmux expands formats in popup arguments, independently of shell quoting.
  tmux display-popup -d "$PWD" -EE -w 90% -h 90% -T ' Workspace controls · q returns to work ' "${launch//#/##}"
}

st_register_command next cmd_next 'use the experimental workspace core' 'next <inspect|commands|invoke|daemon|events> [args]'
st_register_command control cmd_control 'open optional workspace controls; q returns to work' 'control [--workspace <id>]'
cmd_pair() { cmd_next pair "$@"; }
st_register_command pair cmd_pair 'pair with the agent in this worktree tab' 'pair <run|status|send|interrupt> [args]'
cmd_share() { cmd_next share "$@"; }
st_register_command share cmd_share 'share code, diff or output with this worktree agent' 'share <--file path|--diff|--output|--context id> --message <text>'
