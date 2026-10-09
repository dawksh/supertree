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

# Picker preparation enables this only inside its command-substitution process.
# Initializing both values here prevents inherited environment data from being
# treated as an authoritative tmux snapshot.
ST_TMUX_SESSION_ID_SNAPSHOT=''
ST_TMUX_SESSION_ID_SNAPSHOT_ACTIVE=0

# Resolve a readable session name to tmux's opaque, unambiguous session ID.
tmux_session_id() {
  local name=$1 id record snapshot_name
  if [ "$ST_TMUX_SESSION_ID_SNAPSHOT_ACTIVE" = 1 ]; then
    while IFS= read -r record; do
      [ -n "$record" ] || continue
      snapshot_name=${record#*$'\t'}
      snapshot_name=${snapshot_name%%$'\t'*}
      [ "$snapshot_name" = "$name" ] || continue
      printf '%s' "${record%%$'\t'*}"
      return 0
    done <<< "$ST_TMUX_SESSION_ID_SNAPSHOT"
    return 1
  fi
  id=$(tmux list-sessions -F '#{session_id}'$'\t''#{session_name}' 2>/dev/null |
    awk -F '\t' -v wanted="$name" '$2 == wanted { print $1; exit }') || return 1
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
