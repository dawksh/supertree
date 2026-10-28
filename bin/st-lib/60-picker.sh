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
    picker_rows | sort_recent
  fi
}

# ---------------------------------------------------------------- commands

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

  local rows sel dir sess key start
  if [ "$popup" = 1 ]; then
    [ -n "$popup_client" ] || die "no tmux client for tree picker"
    local self
    self=$(st_executable)
    tmux display-popup -c "$popup_client" -d "$PWD" -EE -w 80% -h 70% \
      "$(printf '%q go --picker' "$self")"
    return
  fi

  if [ -n "$query" ]; then
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
          --border --border-label=' Trees ' \
          --prompt='Find tree › ' --preview-window=hidden \
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
      printf 'branch: ' >&2; read -r b
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
  local here main rows row=''
  here=$(git rev-parse --show-toplevel 2>/dev/null || true)
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
    tmux display-popup -c "$popup_client" -EE -w 80% -h 70% "$(printf '%q roots' "$self")"
    return
  fi

  command -v fzf >/dev/null || die "fzf is required for the root picker"
  root=$(printf '%s\n' "$rows" |
    fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
        --border --border-label=' Roots ' --prompt='Find root › ' \
        --bind='esc:abort') || return 0
  root=$(printf '%s' "$root" | cut -f2)
  printf 'branch: ' >&2; read -r branch
  [ -n "$branch" ] || return 0
  cmd_new "$branch" --repo "$root"
}

st_register_command 'open' cmd_open 'open this tree, or the most recent one'
st_register_command 'go' cmd_go 'pick a tree and switch to it' 'go [query]'
st_register_command 'resume' cmd_go 'reopen a tree and resume its agent' 'resume [query]'
st_register_command 'roots' cmd_roots 'pick a known repo and create a tree in it'
