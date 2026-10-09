# shellcheck shell=bash
# supertree core module

die()  { printf 'st: %s\n' "$*" >&2; exit 1; }

info() { printf '\033[2m::\033[0m %s\n' "$*" >&2; }
st_executable() {
  local dir
  case $0 in
    /*) printf '%s' "$0";;
    */*) dir=$(cd "$(dirname "$0")" && pwd); printf '%s/%s' "$dir" "$(basename "$0")";;
    *) command -v -- "$0";;
  esac
}

# A command can validate several sessions. Keep one exact name-to-ID snapshot
# for its lifetime instead of asking the tmux server for the full list each
# time. A cache miss is refreshed once so sessions created or renamed during
# the command are still discoverable.
ST_TMUX_SESSION_SNAPSHOT=''
ST_TMUX_SESSION_SNAPSHOT_READY=0

refresh_tmux_session_snapshot() {
  ST_TMUX_SESSION_SNAPSHOT=$(tmux list-sessions \
    -F '#{session_id}'$'\t''#{session_name}' 2>/dev/null || true)
  ST_TMUX_SESSION_SNAPSHOT_READY=1
}

tmux_session_id_from_snapshot() {
  local wanted=$1 record name
  while IFS= read -r record; do
    [ -n "$record" ] || continue
    name=${record#*$'\t'}
    [ "$name" = "$wanted" ] || continue
    printf '%s' "${record%%$'\t'*}"
    return 0
  done <<EOF
$ST_TMUX_SESSION_SNAPSHOT
EOF
  return 1
}

tmux_session_names_from_snapshot() {
  local record
  while IFS= read -r record; do
    [ -n "$record" ] && printf '%s\n' "${record#*$'\t'}"
  done <<EOF
$ST_TMUX_SESSION_SNAPSHOT
EOF
  return 0
}

# Resolve a readable session name to tmux's opaque, unambiguous session ID.
tmux_session_id() {
  local name=$1 id
  [ "$ST_TMUX_SESSION_SNAPSHOT_READY" = 1 ] || refresh_tmux_session_snapshot
  id=$(tmux_session_id_from_snapshot "$name" || true)
  if [ -z "$id" ]; then
    refresh_tmux_session_snapshot
    id=$(tmux_session_id_from_snapshot "$name" || true)
  fi
  [ -n "$id" ] || return 1
  printf '%s' "$id"
}

# Most tmux commands accept an exact session name as "=name". switch-client is
# different: a target containing ':', '.' or '%' is parsed as a pane target.
# Resolve such names to tmux's opaque session ID so valid Git branch characters
# can never change the target grammar. Names without those characters retain the
# exact-match form, which also keeps compatibility with older tmux versions.
tmux_session_target() {
  local name=$1
  case $name in
    *[:.%]*) tmux_session_id "$name";;
    *) printf '=%s' "$name";;
  esac
}

tmux_has_session() {
  local target
  target=$(tmux_session_target "$1") || return 1
  tmux has-session -t "$target"
}

tmux_kill_session() {
  local target
  target=$(tmux_session_target "$1") || return 1
  tmux kill-session -t "$target"
}
