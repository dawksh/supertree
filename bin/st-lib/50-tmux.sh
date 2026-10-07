# shellcheck shell=bash
# supertree tmux module

window_name() {
  case $1 in
    agent) harness_window;;
    vim|shell) printf '%s' "$1";;
    *) die "unknown window type '$1' (choose from: agent vim shell)";;
  esac
}

configured_windows() {
  local type name seen_types=' ' seen_names=' '
  [ -n "$ST_WINDOWS" ] || die "ST_WINDOWS must contain at least one of: agent vim shell"
  for type in $ST_WINDOWS; do
    case $type in
      agent|vim|shell) ;;
      *) die "unknown window type '$type' in ST_WINDOWS (choose from: agent vim shell)";;
    esac
    case $seen_types in
      *" $type "*) die "duplicate window type '$type' in ST_WINDOWS";;
    esac
    name=$(window_name "$type")
    case $seen_names in
      *" $name "*) die "configured windows resolve to duplicate tmux name '$name'";;
    esac
    seen_types="$seen_types$type "
    seen_names="$seen_names$name "
    printf '%s\n' "$type"
  done
}

window_enabled() {
  local wanted=$1 type
  while read -r type; do [ "$type" = "$wanted" ] && return 0; done <<EOF
$(configured_windows)
EOF
  return 1
}

st_self() { command -v st 2>/dev/null || printf '%s' "$HOME/.local/bin/st"; }

create_window() {
  local sess=$1 dir=$2 type=$3 initial=$4 self name
  self=$(st_self)
  name=$(window_name "$type")
  if [ "$initial" = 1 ]; then
    case $type in
      agent) tmux new-session -d -s "$sess" -c "$dir" -n "$name" "$self _run _agent";;
      vim) tmux new-session -d -s "$sess" -c "$dir" -n "$name" "$self _run nvim";;
      shell) tmux new-session -d -s "$sess" -c "$dir" -n "$name";;
    esac
  else
    case $type in
      agent) tmux new-window -d -t "=$sess" -c "$dir" -n "$name" "$self _run _agent";;
      vim) tmux new-window -d -t "=$sess" -c "$dir" -n "$name" "$self _run nvim";;
      shell) tmux new-window -d -t "=$sess" -c "$dir" -n "$name";;
    esac
  fi
}

build_session() {
  local sess=$1 dir=$2 windows type first_name='' initial=1 branch label
  branch=$(git -C "$dir" branch --show-current 2>/dev/null || true)
  label="$(basename "$(main_worktree "$dir")")/${branch:-(detached)}"
  if tmux has-session -t "=$sess" 2>/dev/null; then
    tmux set-option -t "$sess" @supertree_label "$label"
    return 0
  fi
  windows=$(configured_windows)
  while read -r type; do
    [ -n "$type" ] || continue
    [ -n "$first_name" ] || first_name=$(window_name "$type")
    create_window "$sess" "$dir" "$type" "$initial"
    if [ "$type" = agent ]; then
      tmux set-option -t "$sess" @st_agent_window "$(window_name agent)"
    fi
    initial=0
  done <<EOF
$windows
EOF
  tmux set-option -t "$sess" @supertree_label "$label"
  tmux select-window -t "=$sess:$first_name"
}

remember_recent() {
  local sess=$1 tmp
  mkdir -p "$ST_STATE"
  tmp=$(mktemp "$ST_STATE/.recent.XXXXXX")
  {
    printf '%s\n' "$sess"
    if [ -f "$ST_STATE/recent" ]; then
      grep -vxF -- "$sess" "$ST_STATE/recent" || true
    fi
  } > "$tmp"
  mv "$tmp" "$ST_STATE/recent"
}

sort_recent() {
  local recent="$ST_STATE/recent"
  [ -f "$recent" ] || recent=/dev/null
  awk -F '\t' 'FILENAME == ARGV[1] { rank[$0] = ++n; next }
    { key = $3; print (key in rank ? rank[key] : 100000 + FNR) "\t" $0 }' \
    "$recent" - | sort -n -k1,1 | cut -f2-
}

tty_key() { printf '%s' "$1" | tr '/' '-'; }

remember_origin() {
  local tty cur window record
  tty=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#{client_tty}' 2>/dev/null) || return 0
  cur=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null) || return 0
  window=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#{window_id}' 2>/dev/null || true)
  [ -n "$tty" ] && [ -n "$cur" ] && [ "$cur" != "$1" ] || return 0
  mkdir -p "$ST_STATE/origin"
  # Each line is session<TAB>window-id, oldest first. A legacy one-field
  # origin file is already a valid one-entry history. If tmux cannot report a
  # window ID, retain the session so leave still has a safe fallback.
  record=$cur
  [ -z "$window" ] || record="$record"$'\t'"$window"
  printf '%s\n' "$record" >> "$ST_STATE/origin/$(tty_key "$tty")"
}

pop_live_origin() {
  local tty=$1 cur=$2 file record candidate tmp
  file="$ST_STATE/origin/$(tty_key "$tty")"
  [ -f "$file" ] || return 1

  while [ -s "$file" ]; do
    record=$(tail -n 1 "$file")
    candidate=${record%%$'\t'*}
    tmp=$(mktemp "$ST_STATE/origin/.pop.XXXXXX") || return 1
    if ! sed '$d' "$file" > "$tmp"; then
      rm -f -- "$tmp"
      return 1
    fi
    mv "$tmp" "$file"

    [ -n "$candidate" ] && [ "$candidate" != "$cur" ] || continue
    if tmux has-session -t "=$candidate" 2>/dev/null; then
      printf '%s\n' "$record"
      return 0
    fi
  done
  return 1
}

switch_to_origin() {
  local tty=$1 cur=$2 record origin window
  while record=$(pop_live_origin "$tty" "$cur"); do
    origin=${record%%$'\t'*}
    window=''
    case $record in *$'\t'*) window=${record#*$'\t'};; esac
    if [ -n "$window" ] &&
       tmux switch-client -c "$tty" -t "=$origin:$window" 2>/dev/null; then
      return 0
    fi
    tmux switch-client -c "$tty" -t "=$origin" 2>/dev/null && return 0
  done
  return 1
}

evacuate_clients() {
  local sess=$1 ttys tty other
  ttys=$(tmux list-clients -t "=$sess" -F '#{client_tty}' 2>/dev/null) || return 0
  [ -n "$ttys" ] || return 0
  while read -r tty; do
    [ -n "$tty" ] || continue
    switch_to_origin "$tty" "$sess" 2>/dev/null && continue
    # fall back to the most recently used other session
    other=$(tmux list-sessions -F '#{session_last_attached}|#{session_name}' 2>/dev/null |
      sort -rn | cut -d'|' -f2- | grep -vxF -- "$sess" | head -1) || other=""
    [ -n "$other" ] && tmux switch-client -c "$tty" -t "=$other" 2>/dev/null || true
  done <<EOF
$ttys
EOF
}

attach() {
  local sess=$1
  remember_recent "$sess"
  if [ -n "${TMUX:-}" ]; then
    remember_origin "$sess"
    tmux switch-client -t "=$sess"
  else
    tmux attach-session -t "=$sess"
  fi
}

known_sessions() {
  list_trees | cut -f4
}

subtree_sessions() {
  local repo branch path sess main
  list_trees | while IFS=$'\t' read -r repo branch path sess; do
    main=$(main_worktree "$path")
    [ "$path" != "$main" ] || continue
    printf '%s\n' "$sess"
  done
}

live_sessions() {
  local s
  known_sessions | while read -r s; do
    [ -n "$s" ] || continue
    tmux has-session -t "=$s" 2>/dev/null && printf '%s\n' "$s"
  done
}

live_subtree_sessions() {
  local s
  subtree_sessions | while read -r s; do
    [ -n "$s" ] || continue
    tmux has-session -t "=$s" 2>/dev/null && printf '%s\n' "$s"
  done
}

ensure_main_fallbacks() {
  local repo branch path subtree main main_branch main_sess
  list_trees | while IFS=$'\t' read -r repo branch path subtree; do
    main=$(main_worktree "$path")
    [ "$path" != "$main" ] || continue
    tmux has-session -t "=$subtree" 2>/dev/null || continue
    main_branch=$(git -C "$main" branch --show-current)
    [ -n "$main_branch" ] || main_branch='(detached)'
    main_sess=$(tree_session "$repo" "$main_branch")
    if ! tmux has-session -t "=$main_sess" 2>/dev/null; then
      tmux new-session -d -s "$main_sess" -c "$main" -n shell
      tmux set-option -t "$main_sess" @supertree_label "$(basename "$main")/$main_branch"
    fi
  done
}

cmd_down() {
  local yes=0 target="" s live current='' close_current=0
  while [ $# -gt 0 ]; do
    case $1 in
      -y|--yes) yes=1; shift;;
      --all) target=--all; shift;;
      --subtrees) target=--subtrees; shift;;
      *) target=$1; shift;;
    esac
  done

  if [ "$target" = --all ] || [ "$target" = --subtrees ]; then
    if [ "$target" = --all ]; then live=$(live_sessions)
    else live=$(live_subtree_sessions); fi
    [ -n "$live" ] || { info "no matching tree sessions open"; return 0; }
    printf 'will close:\n' >&2
    while IFS= read -r s; do printf '  %s\n' "$(label_for_session "$s")" >&2; done <<EOF
$live
EOF
    if [ "$yes" = 0 ]; then
      printf 'unsaved editor buffers are lost. proceed? [y/N] ' >&2; read -r ans
      case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac
    fi
    [ "$target" != --subtrees ] || ensure_main_fallbacks
    if [ -n "${TMUX:-}" ]; then
      current=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null || true)
    fi
    # Killing our own session kills this process, so close it last.
    while IFS= read -r s; do
      evacuate_clients "$s"
      if [ "$s" = "$current" ]; then close_current=1; else tmux kill-session -t "=$s"; fi
    done <<EOF
$live
EOF
    info "closed selected tree sessions (worktrees kept)"
    [ "$close_current" = 0 ] || tmux kill-session -t "=$current"
    return 0
  fi

  local all
  all=$(known_sessions)

  if [ -n "$target" ]; then
    s=$(session_for_label "$target" || true)
    if [ -z "$s" ]; then
      case $'\n'"$all"$'\n' in
        *$'\n'"$target"$'\n'*) s=$target;;
        *) s=$(printf '%s\n' "$all" | grep -i -- "$target" | head -1) || true;;
      esac
    fi
    [ -n "$s" ] || die "no tree matching: $target"
  elif [ -n "${TMUX:-}" ]; then
    s=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S')
  else
    die "usage: st down [branch|--all]  (no branch only works inside a tree session)"
  fi

  # guard: only ever kill sessions that belong to a known worktree
  case $'\n'"$all"$'\n' in
    *$'\n'"$s"$'\n'*) ;;
    *) die "this is not a supertree session; refusing to close it";;
  esac

  tmux has-session -t "=$s" 2>/dev/null || { info "$(label_for_session "$s") already closed"; return 0; }
  evacuate_clients "$s"
  tmux kill-session -t "=$s"
  info "closed $(label_for_session "$s") (worktree kept — 'st resume' brings it back)"
}

cmd_toggle() {
  local pane cur first second first_name target
  pane=${TMUX_PANE:-}
  if [ -n "$pane" ]; then
    cur=$(tmux display-message -p -t "$pane" '#W')
  else
    cur=$(tmux display-message -p '#W')
  fi
  first=$(configured_windows | sed -n '1p')
  second=$(configured_windows | sed -n '2p')
  [ -n "$second" ] || { info "toggle needs at least two configured windows"; return 0; }
  first_name=$(window_name "$first")
  if [ "$cur" = "$first_name" ]; then target=$second; else target=$first; fi
  select_window_type "$target"
}

select_window_type() {
  local type=$1 pane sess name dir
  window_enabled "$type" || { info "$type window is not enabled in ST_WINDOWS"; return 0; }
  pane=${TMUX_PANE:-}
  if [ -n "$pane" ]; then sess=$(tmux display-message -p -t "$pane" '#S')
  else sess=$(tmux display-message -p '#S'); fi
  name=$(window_name "$type")
  tmux select-window -t "=$sess:$name" 2>/dev/null && return 0
  dir=$(tmux display-message -p -t "${pane:-$sess}" '#{pane_current_path}')
  create_window "$sess" "$dir" "$type" 0
  tmux select-window -t "=$sess:$name"
}

cmd_window() {
  local slot=${1:-} type
  case $slot in ''|*[!0-9]*|0) die "usage: st window <positive slot>";; esac
  type=$(configured_windows | sed -n "${slot}p")
  [ -n "$type" ] || { info "no window configured for slot $slot"; return 0; }
  select_window_type "$type"
}

cmd_last() {
  local current trees sess path
  [ -n "${TMUX:-}" ] || die "st last only works inside tmux"
  [ -f "$ST_STATE/recent" ] || { info "no other tree opened yet"; return 0; }
  current=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null || true)
  trees=$(list_trees)
  # Record the current tree so the next M-Tab comes straight back.
  if printf '%s\n' "$trees" | cut -f4 | grep -qxF -- "$current"; then
    remember_recent "$current"
  fi
  while IFS= read -r sess; do
    [ -n "$sess" ] && [ "$sess" != "$current" ] || continue
    path=$(printf '%s\n' "$trees" | awk -F '\t' -v s="$sess" '$4 == s && !found { print $3; found = 1 }')
    [ -n "$path" ] || continue
    build_session "$sess" "$path"
    attach "$sess"
    return 0
  done < "$ST_STATE/recent"
  info "no other tree opened yet"
}

cmd_agent() {
  select_window_type agent
}

cmd_leave() {
  local tty=${1:-} cur
  [ -n "${TMUX:-}" ] || die "st leave only works inside tmux"
  [ -n "$tty" ] || tty=$(tmux display-message -p '#{client_tty}')
  cur=$(tmux display-message -p -c "$tty" '#S' 2>/dev/null || true)
  # Unwind nested switches; detach only when no live origin remains.
  switch_to_origin "$tty" "$cur" && return 0
  tmux detach-client -t "$tty"
}

st_register_command 'down' cmd_down 'close sessions; keep worktrees' 'down [branch|--subtrees|--all]'
st_register_command 'toggle' cmd_toggle ''
st_register_command 'window' cmd_window ''
st_register_command 'last' cmd_last 'switch to the previous tree'
st_register_command 'agent' cmd_agent ''
st_register_command 'leave' cmd_leave ''
st_register_command '_sessions' known_sessions ''
