# shellcheck shell=bash
# supertree agent module

harness_window() {
  case $ST_HARNESS in
    claude|codex|openrouter) printf '%s' "$ST_HARNESS";;
    *[!A-Za-z0-9_-]*|'') die "invalid ST_HARNESS: $ST_HARNESS";;
    *) [ -n "$ST_HARNESS_COMMAND" ] || die "unknown harness '$ST_HARNESS' (set ST_HARNESS_COMMAND)"
       printf '%s' "$ST_HARNESS";;
  esac
}

harness_bin() {
  case $ST_HARNESS in
    claude) printf 'claude';;
    codex) printf 'codex';;
    openrouter) printf 'opencode';;
    *) harness_window >/dev/null; printf '%s' "${ST_HARNESS_COMMAND%% *}";;
  esac
}

harness_label() {
  case $ST_HARNESS in
    claude) printf 'Claude Code';;
    codex) printf 'Codex';;
    openrouter) printf 'OpenRouter via OpenCode';;
    *) harness_window;;
  esac
}

agent_pane() {
  local sess=$1 name
  name=$(tmux show-options -qv -t "$sess" @st_agent_window 2>/dev/null) || name=""
  [ -n "$name" ] || name=$(harness_window)
  tmux list-panes -t "=$sess:$name" -F '#{pane_id}' 2>/dev/null | head -1
}

agent_needs_input() {
  local pane=$1 screen
  screen=$(tmux capture-pane -p -t "$pane" -S -12 2>/dev/null) || return 1
  printf '%s\n' "$screen" | tail -8 | grep -Eq \
    '^[[:space:]]*(›|❯)([[:space:]]|$)|\[[yY]/[nN]\][[:space:]]*$'
}

agent_status() {
  local sess=$1 pane state command
  if ! tmux has-session -t "=$sess" 2>/dev/null; then
    printf 'closed'; return
  fi
  pane=$(agent_pane "$sess")
  [ -n "$pane" ] || { printf 'done'; return; }
  state=$(tmux show-options -pqv -t "$pane" @st_agent_state 2>/dev/null) || state=""
  if [ "$state" = done ]; then printf 'done'; return; fi
  if [ -z "$state" ]; then
    # Sessions created before status tracking have no pane option yet.
    command=$(tmux display-message -p -t "$pane" '#{pane_current_command}' 2>/dev/null) || command=""
    case $command in
      "$(basename "$(harness_bin)")"|node|bun) ;;
      *) printf 'done'; return;;
    esac
  fi
  if agent_needs_input "$pane"; then printf 'input'; else printf 'running'; fi
}

set_agent_status() {
  [ -n "${TMUX_PANE:-}" ] || return 0
  tmux set-option -pq -t "$TMUX_PANE" @st_agent_state "$1" 2>/dev/null || true
}

cmd_run() {
  case ${1:-} in
    _agent) cmd_harness || true;;
    _claude) ST_HARNESS=claude cmd_harness || true;; # old live sessions
    *) "$@" || true;;
  esac
  exec "${SHELL:-/bin/zsh}" -i
}

cmd_harness() {
  local repo slug marker legacy old_marker bin main branch current_tree
  main=$(main_worktree .)
  repo=$(repo_key "$main")
  branch=$(git branch --show-current 2>/dev/null || true)
  slug=$(branch_key "${branch:-(detached)}")
  harness_window >/dev/null
  bin=$(harness_bin)
  command -v "$bin" >/dev/null 2>&1 || die "$(harness_label) is selected but '$bin' is not installed"
  marker="$ST_STATE/seen/$repo/$slug.$ST_HARNESS"
  legacy="$ST_STATE/seen/$(basename "$main")/$(slugify "$branch")"
  old_marker="$legacy.$ST_HARNESS"
  current_tree=$(branch_worktree "$main" "$branch" || true)
  if [ -z "$current_tree" ] ||
     [ ! "$current_tree" -ef "$(tree_dir "$(basename "$main")" "$(slugify "$branch")")" ]; then
    legacy=''
    old_marker=''
  fi
  mkdir -p "$(dirname "$marker")"
  set_agent_status running
  if [ -f "$marker" ] || { [ -n "$old_marker" ] && [ -f "$old_marker" ]; } ||
     { [ "$ST_HARNESS" = claude ] && [ -n "$legacy" ] && [ -f "$legacy" ]; }; then
    case $ST_HARNESS in
      claude) claude --continue || claude;;
      codex) codex resume --last || codex;;
      openrouter) opencode --continue || opencode;;
      *) if [ -n "$ST_HARNESS_RESUME_COMMAND" ]; then
           eval "$ST_HARNESS_RESUME_COMMAND" || eval "$ST_HARNESS_COMMAND"
         else
           eval "$ST_HARNESS_COMMAND"
         fi;;
    esac
  else
    : > "$marker"
    case $ST_HARNESS in
      claude) claude;;
      codex) codex;;
      openrouter) opencode;;
      *) eval "$ST_HARNESS_COMMAND";;
    esac
  fi
  set_agent_status done
}

cmd_legacy_claude() { ST_HARNESS=claude cmd_harness "$@"; }

st_register_command '_run' cmd_run ''
st_register_command '_agent' cmd_harness ''
st_register_command '_claude' cmd_legacy_claude ''
