# shellcheck shell=bash
# supertree picker module

picker_rows() {
  local trees statuses
  if [ $# -gt 0 ]; then trees=$1; else trees=$(list_trees); fi
  statuses=$(agent_statuses)
  printf '%s\n' "$trees" | awk -F '\t' '
    FILENAME == ARGV[1] { if ($1 != "") status_of[$1] = $2; next }
    $0 != "" {
      s = $4
      repo_label = $1
      sub(/-[^-]*$/, "", repo_label)
      label = repo_label "/" $2
      n++; labels[n] = label
      row[n] = ((s in status_of) ? status_of[s] : "closed") "\t" $3 "\t" s
      if (length(label) > width) width = length(label)
    }
    END { for (i = 1; i <= n; i++) printf "%-" width "s  %s\n", labels[i], row[i] }
  ' <(printf '%s\n' "$statuses") -
}

picker_choices() {
  if declare -F cmd_new >/dev/null; then printf '+ new branch…\t\t\n'; fi
  if [ "${ST_PICKER_ROWS+x}" = x ]; then
    printf '%s\n' "$ST_PICKER_ROWS"
  else
    picker_rows | sort_recent | awk -F '\t' '
      {
        label = $1; sub(/  +[^ ]+$/, "", label)
        state = $1; sub(/^.*  +/, "", state)
        if (state == "running") state = "● Running"
        else if (state == "input") state = "◐ Needs input"
        else if (state == "done") state = "○ Finished"
        else state = "· Closed"
        printf "%s  %s\t%s\t%s\n", label, state, $2, $3
      }'
  fi
}

# ---------------------------------------------------------------- commands

# Bare `st` resumes the last tree. With no usable recent tree, enter home with
# the sidebar focused so the tree list is the first thing to interact with.
cmd_start() {
  [ $# -eq 0 ] || die "usage: st"
  local trees row='' sess here main
  trees=$(list_trees)
  if [ -s "$ST_STATE/recent" ]; then
    while IFS= read -r sess; do
      [ -n "$sess" ] || continue
      row=$(printf '%s\n' "$trees" | awk -F '\t' -v s="$sess" '$4 == s { print; exit }')
      [ -z "$row" ] || break
    done < "$ST_STATE/recent"
  fi
  if [ -z "$row" ]; then
    here=$(git rev-parse --show-toplevel 2>/dev/null || true)
    if [ -n "$here" ]; then
      main=$(main_worktree .) || main=''
      [ -z "$main" ] || register_repo "$main"
      trees=$(list_trees)
      row=$(printf '%s\n' "$trees" | awk -F '\t' -v p="$here" '$3 == p { print; exit }')
    fi
  fi
  if [ -n "$row" ]; then
    sess=$(printf '%s' "$row" | cut -f4)
    build_session "$sess" "$(printf '%s' "$row" | cut -f3)"
    attach "$sess"
  else
    cmd_home --picker
  fi
}

cmd_go() {
  local query="" popup=0 popup_client=""
  while [ $# -gt 0 ]; do
    case $1 in
      --picker) shift;;
      --popup) [ $# -ge 2 ] || die "usage: st go --popup <tmux-client>"
               popup=1; popup_client=$2; shift 2;;
      *) query=$1; shift;;
    esac
  done

  local rows sel dir sess key start live_dir
  if [ "$popup" = 1 ]; then
    [ -n "$popup_client" ] || die "no tmux client for tree picker"
    local self
    self=$(st_executable)
    tmux display-popup -c "$popup_client" -d "$PWD" -EE -w 72% -h 60% \
      "$(printf '%q go --picker' "$self")"
    return
  fi

  if [ -n "$query" ]; then
    # Exact live-session names already carry their tree path. A partial query
    # still needs the full picker rows to choose the first match.
    if [ "${ST_PICKER_ROWS+x}" != x ]; then
      case $query in
        */*)
          live_dir=$(tmux list-windows -a -F '#{session_name}'$'\t''#{@st_tree_dir}' 2>/dev/null |
            awk -F '\t' -v q="$query" '$1 == q && $2 != "" { print $2; exit }')
          if [ -d "$live_dir" ]; then
            build_session "$query" "$live_dir"
            attach "$query"
            return 0
          fi;;
      esac
    fi
    if [ "${ST_PICKER_ROWS+x}" = x ]; then rows=$ST_PICKER_ROWS
    else rows=$(picker_rows | sort_recent); fi
    [ -n "$rows" ] || die "no worktrees known yet — run 'st new <branch>' inside a repo"
    sel=$(printf '%s\n' "$rows" | ST_QUERY=$query awk -F '\t' '
      { split($1, cols, " ") }
      index(tolower(cols[1]), tolower(ENVIRON["ST_QUERY"])) { print; exit }')
    [ -n "$sel" ] || die "no tree matching: $query"
  else
    command -v fzf >/dev/null || die "fzf is required for the tree picker"
    [ -f "$ST_REPOS" ] || [ "${ST_PICKER_ROWS+x}" = x ] ||
      die "no worktrees known yet — run 'st new <branch>' inside a repo"
    start=1
    if declare -F cmd_new >/dev/null; then
      start=2
    fi
    # Cursor starts on the most recent tree so Enter still reopens it.
    sel=$(fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
          --no-sort --bind="load:pos($start)" \
          --border=rounded --border-label=' Trees ' \
          --prompt='Search › ' --header='Enter open · Ctrl-D remove · Esc close' \
          --expect=ctrl-d --bind='esc:abort' < <(picker_choices)) || return 0
    key=${sel%%$'\n'*}
    sel=${sel#*$'\n'}
    if [ "$key" = ctrl-d ]; then
      declare -F remove_tree_at >/dev/null || { info "remove module is unavailable"; return 0; }
      case $sel in '+ new branch'*) return 0;; esac
      remove_tree_at "$(printf '%s' "$sel" | cut -f2)"
      return
    fi
  fi

  case $sel in
    '+ new branch'*)
      local b r root
      printf 'New branch name › ' >&2; read -r b
      [ -n "$b" ] || return 0
      r=$(sort -u "$ST_REPOS" | while IFS= read -r root; do
        [ -d "$root" ] || continue
        printf '%s\n' "$root"
      done)
      [ -n "$r" ] || die "no roots known yet — run 'st new <branch>' inside a repo"
      if [ "$(printf '%s\n' "$r" | wc -l)" -gt 1 ]; then
        r=$(printf '%s\n' "$r" | fzf --prompt='repo> ' --height=100% --reverse) || return 0
      fi
      cmd_new "$b" --repo "$r"
      return;;
  esac

  dir=$(printf '%s' "$sel" | cut -f2)
  sess=$(printf '%s' "$sel" | cut -f3)
  build_session "$sess" "$dir"
  attach "$sess"
}

# Direct entry: the tree you are standing in, else the most recently opened one.
cmd_open() {
  local here main rows row='' live
  here=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [ -n "$here" ]; then
    # A live tree already records its checkout path on each window. Reuse it
    # without discovering every repository; home may share the same cwd.
    live=$(tmux list-windows -a -F '#{session_name}'$'\t''#{@st_tree_dir}' 2>/dev/null |
      awk -F '\t' -v p="$here" -v home="$ST_HOME_SESSION" '$1 != home && $2 == p { print $1; exit }')
    if [ -n "$live" ]; then attach "$live"; return 0; fi
  fi
  main=$(main_worktree .) || main=''
  [ -z "$main" ] || register_repo "$main"
  rows=$(list_trees | cut -f2-)
  [ -n "$rows" ] || die "no worktrees known yet — run 'st new <branch>' inside a repo"
  [ -z "$here" ] || row=$(printf '%s\n' "$rows" | awk -F '\t' -v p="$here" '$2 == p' | head -1)
  [ -n "$row" ] || row=$(printf '%s\n' "$rows" | sort_recent | head -1)
  build_session "$(printf '%s' "$row" | cut -f3)" "$(printf '%s' "$row" | cut -f2)"
  attach "$(printf '%s' "$row" | cut -f3)"
}

cmd_roots() {
  local popup_client='' rows root label branch self
  case ${1:-} in
    --popup) [ $# -ge 2 ] || die "usage: st roots --popup <tmux-client>"; popup_client=$2;;
    '') ;;
    *) die "usage: st roots";;
  esac
  [ -f "$ST_REPOS" ] || die "no roots known yet — run 'st new <branch>' inside a repo"
  rows=$(sort -u "$ST_REPOS" | while IFS= read -r root; do
    [ -d "$root" ] || continue
    case $root in ("$HOME"/*) label="~${root#"$HOME"}";; (*) label=$root;; esac
    printf '%s\t%s\n' "$label" "$root"
  done)
  [ -n "$rows" ] || die "no roots known yet — run 'st new <branch>' inside a repo"

  if [ -n "$popup_client" ]; then
    self=$(st_executable)
    tmux display-popup -c "$popup_client" -EE -w 72% -h 60% "$(printf '%q roots' "$self")"
    return
  fi

  command -v fzf >/dev/null || die "fzf is required for the root picker"
  root=$(printf '%s\n' "$rows" |
    fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
        --border=rounded --border-label=' Repositories ' --prompt='Search › ' \
        --header='Enter select · Esc close' \
        --bind='esc:abort') || return 0
  root=$(printf '%s' "$root" | cut -f2)
  printf 'New branch name › ' >&2; read -r branch
  [ -n "$branch" ] || return 0
  cmd_new "$branch" --repo "$root"
}

st_register_command 'start' cmd_start ''
st_register_command 'open' cmd_open 'open this tree, or the most recent one'
st_register_command 'go' cmd_go 'pick a tree and switch to it' 'go [query]'
st_register_command 'resume' cmd_go 'reopen a tree and resume its agent' 'resume [query]'
st_register_command 'roots' cmd_roots 'pick a known repo and create a tree in it'
