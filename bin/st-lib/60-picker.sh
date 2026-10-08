# shellcheck shell=bash
# supertree picker module

picker_rows() {
  local trees panes sessions owned format name bin width label status path sess pane repo branch expected actual legacy record
  trees=$(list_trees)
  sessions=$(tmux list-sessions -F '#{session_name}'$'\t''#{@supertree_identity}'$'\t''#{@supertree_label}' 2>/dev/null || true)
  owned=$(while IFS=$'\t' read -r repo branch path sess; do
    record=$(printf '%s\n' "$sessions" | awk -F '\t' -v s="$sess" '$1 == s { print; exit }')
    [ -n "$record" ] || continue
    IFS=$'\t' read -r _ actual legacy <<< "$record"
    expected=$(tree_session_identity "$path" "$branch")
    if [ "$actual" = "$expected" ]; then
      printf '%s\n' "$sess"
    elif [ -z "$actual" ] && [ "$legacy" = "$(legacy_session_label "$path" "$branch")" ] &&
         claim_tree_session "$sess" "$repo" "$branch" "$path"; then
      printf '%s\n' "$sess"
    fi
  done <<< "$trees")
  format='#{session_name}'$'\t''#{window_name}'$'\t''#{pane_id}'$'\t''#{@st_agent_state}'$'\t''#{@st_agent_window}'$'\t''#{pane_current_command}'
  panes=$(tmux list-panes -a -F "$format" 2>/dev/null) || panes=''
  name=$(harness_window)
  bin=$(harness_bin)
  bin=${bin##*/}

  printf '%s\n' "$trees" | awk -F '\t' -v default_name="$name" -v bin="$bin" '
    FILENAME == ARGV[1] {
      owned[$1] = 1
      next
    }
    FILENAME == ARGV[2] {
      s = $1
      if (s == "" || !(s in owned)) next
      live[s] = 1
      if ($5 != "") agent_window[s] = $5
      wanted = agent_window[s] != "" ? agent_window[s] : default_name
      if ($2 == wanted && !(s in agent_pane)) {
        agent_pane[s] = $3
        agent_state[s] = $4
        agent_command[s] = $6
      }
      next
    }
    $0 != "" {
      s = $4
      repo_label = $1
      sub(/-[^-]*$/, "", repo_label)
      label = repo_label "/" $2
      if (!(s in live)) { status = "closed" }
      else {
        status = "done"
        if (s in agent_pane && (agent_state[s] == "running" ||
            (agent_state[s] == "" && (agent_command[s] == bin ||
             agent_command[s] == "node" || agent_command[s] == "bun"))))
          status = "running"
      }
      n++; row[n] = label "\t" status "\t" $3 "\t" s "\t" agent_pane[s]
      if (length(label) > width) width = length(label)
    }
    END { for (i = 1; i <= n; i++) print width "\t" row[i] }
  ' <(printf '%s\n' "$owned") <(printf '%s\n' "$panes") - | while IFS=$'\t' read -r width label status path sess pane; do
    if [ "$status" = running ] && [ -n "$pane" ] && agent_needs_input "$pane"; then
      status=input
    fi
    printf '%-*s  %s\t%s\t%s\n' "$width" "$label" "$status" "$path" "$sess"
  done
}

picker_query_match() {
  local rows=$1 query=$2
  ST_PICKER_QUERY=$query awk -F '\t' '
    BEGIN { query = tolower(ENVIRON["ST_PICKER_QUERY"]) }
    {
      row = $0
      label = $1
      sub(/[[:space:]]+(closed|done|running|input)$/, "", label)
      folded_label = tolower(label)
      branch = folded_label
      sub(/^[^\/]*\//, "", branch)

      if (folded_label == query && exact_label == "") exact_label = row
      else if (branch == query && exact_branch == "") exact_branch = row
      else if (index(folded_label, query) && partial == "") partial = row
    }
    END {
      if (exact_label != "") print exact_label
      else if (exact_branch != "") print exact_branch
      else if (partial != "") print partial
    }
  ' <<EOF
$rows
EOF
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

  local rows sel dir sess key branch main choices
  if [ "${ST_PICKER_ROWS+x}" = x ]; then rows=$ST_PICKER_ROWS
  else rows=$(picker_rows | sort_recent); fi

  [ -n "$rows" ] || die "no worktrees known yet — run 'st new <branch>' inside a repo"

  if [ "$popup" = 1 ]; then
    [ -n "$popup_client" ] || die "no tmux client for tree picker"
    local self
    self=$(st_executable)
    tmux display-popup -c "$popup_client" -d "$PWD" -E -w 80% -h 70% \
      -e "ST_PICKER_ROWS=$rows" "$self go --picker"
    return
  fi

  if [ -n "$query" ]; then
    sel=$(picker_query_match "$rows" "$query")
    [ -n "$sel" ] || die "no tree matching: $query"
  else
    choices=$rows
    if declare -F cmd_new >/dev/null; then
      choices=$(printf '%s\n+ new branch…\t\t\n' "$rows")
    fi
    sel=$(printf '%s\n' "$choices" |
      fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
          --no-sort --sync \
          --border --border-label=' Trees ' \
          --prompt='Find tree › ' --preview-window=hidden \
          --expect=ctrl-d --bind='esc:abort') || return 0
    key=${sel%%$'\n'*}
    sel=${sel#*$'\n'}
    if [ "$key" = ctrl-d ]; then
      declare -F cmd_rm >/dev/null || { info "remove module is unavailable"; return 0; }
      case $sel in '+ new branch'*) return 0;; esac
      dir=$(printf '%s' "$sel" | cut -f2)
      branch=$(git -C "$dir" branch --show-current)
      [ -n "$branch" ] || die "cannot delete a detached worktree from the picker"
      main=$(main_worktree "$dir")
      [ "$dir" != "$main" ] || die "cannot delete the main worktree"
      cmd_rm "$branch" --repo "$main"
      return
    fi
  fi

  case $sel in
    '+ new branch'*)
      local b r
      printf 'branch: ' >&2; read -r b
      [ -n "$b" ] || return 0
      r=$(sort -u "$ST_REPOS")
      if [ "$(printf '%s\n' "$r" | wc -l)" -gt 1 ]; then
        r=$(printf '%s\n' "$r" | fzf --prompt='repo> ' --height=100% --reverse) || return 0
      fi
      cmd_new "$b" --repo "$r"
      return;;
  esac

  dir=$(printf '%s' "$sel" | cut -f2)
  sess=$(printf '%s' "$sel" | cut -f3)
  build_session "$sess" "$dir"
  attach "$sess" "$dir"
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
    tmux display-popup -c "$popup_client" -E -w 80% -h 70% "$self roots"
    return
  fi

  root=$(printf '%s\n' "$rows" |
    fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
        --border --border-label=' Roots ' --prompt='Find root › ' \
        --bind='esc:abort') || return 0
  root=$(printf '%s' "$root" | cut -f2)
  printf 'branch: ' >&2; read -r branch
  [ -n "$branch" ] || return 0
  cmd_new "$branch" --repo "$root"
}

st_register_command 'go' cmd_go 'pick a tree and switch to it' 'go [query]'
st_register_command 'resume' cmd_go 'reopen a tree and resume its agent' 'resume [query]'
st_register_command 'roots' cmd_roots 'pick a known repo and create a tree in it'
