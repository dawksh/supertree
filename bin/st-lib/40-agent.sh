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
  name=$(tmux show-options -qv -t "=$sess:" @st_agent_window 2>/dev/null) || name=""
  [ -n "$name" ] || name=$(harness_window)
  tmux list-panes -t "=$sess:$name" -f '#{!=:#{@st_sidebar},1}' -F '#{pane_id}' 2>/dev/null | head -1
}

agent_needs_input() {
  local pane=$1 screen
  screen=$(tmux capture-pane -p -t "$pane" -S -12 2>/dev/null) || return 1
  printf '%s\n' "$screen" | tail -8 | grep -Eq \
    '^[[:space:]]*(›|❯)([[:space:]]|$)|\[[yY]/[nN]\][[:space:]]*$'
}

agent_status() {
  local sess=$1 tab=$'\t' rows row wanted='' pane='' state='' command='' bin format
  format="#{@st_sidebar}$tab#{@st_agent_window}$tab#{window_name}$tab#{pane_id}$tab#{@st_agent_state}$tab#{pane_current_command}"
  rows=$(tmux list-panes -s -t "=$sess" -F "$format" 2>/dev/null) || { printf 'closed'; return; }
  # Split by hand: `IFS=$'\t' read` merges empty fields and shifts the columns.
  while IFS= read -r row; do
    [ "${row%%"$tab"*}" != 1 ] || continue
    row=${row#*"$tab"}
    [ -n "$wanted" ] || wanted=${row%%"$tab"*}
    [ -n "$wanted" ] || wanted=$(harness_window)
    row=${row#*"$tab"}
    [ "${row%%"$tab"*}" = "$wanted" ] || continue
    row=${row#*"$tab"}; pane=${row%%"$tab"*}
    row=${row#*"$tab"}; state=${row%%"$tab"*}
    command=${row#*"$tab"}
    break
  done <<EOF
$rows
EOF
  [ -n "$pane" ] || { printf 'done'; return; }
  if [ "$state" = done ]; then printf 'done'; return; fi
  if [ -z "$state" ]; then
    # Sessions created before status tracking have no pane option yet.
    bin=$(harness_bin)
    case $command in
      "${bin##*/}"|node|bun) ;;
      *) printf 'done'; return;;
    esac
  fi
  if agent_needs_input "$pane"; then printf 'input'; else printf 'running'; fi
}

agent_tail() {
  local sess=$1 lines=${2:-8} pane
  tmux has-session -t "=$sess" 2>/dev/null || return 0
  pane=$(agent_pane "$sess")
  [ -n "$pane" ] || return 0
  tmux capture-pane -p -t "$pane" -S -60 2>/dev/null |
    awk 'NF { last = NR } { line[NR] = $0 } END { for (i = 1; i <= last; i++) print line[i] }' |
    tail -n "$lines"
}

agent_statuses() {
  local format panes name bin sess status pane
  format='#{session_name}'$'\t''#{window_name}'$'\t''#{pane_id}'$'\t''#{@st_agent_state}'$'\t''#{@st_agent_window}'$'\t''#{pane_current_command}'$'\t''#{@st_sidebar}'
  panes=$(tmux list-panes -a -F "$format" 2>/dev/null) || panes=''
  [ -n "$panes" ] || return 0
  name=$(harness_window)
  bin=$(harness_bin)
  bin=${bin##*/}
  # \037 separates fields because read collapses consecutive tabs, shifting empty ones.
  printf '%s\n' "$panes" | awk -F '\t' -v default_name="$name" -v bin="$bin" '
    $1 == "" || $7 == 1 { next }
    !($1 in seen) { seen[$1] = 1; order[++n] = $1 }
    $5 != "" { agent_window[$1] = $5 }
    {
      wanted = ($1 in agent_window) ? agent_window[$1] : default_name
      if ($2 == wanted && !($1 in pane)) { pane[$1] = $3; state[$1] = $4; command[$1] = $6 }
    }
    END {
      for (i = 1; i <= n; i++) {
        s = order[i]; status = "running"
        if (!(s in pane) || state[s] == "done") status = "done"
        else if (state[s] == "" && command[s] != bin && command[s] != "node" && command[s] != "bun") status = "done"
        printf "%s\037%s\037%s\n", s, status, pane[s]
      }
    }' | while IFS=$'\037' read -r sess status pane; do
    if [ "$status" = running ] && agent_needs_input "$pane"; then status=input; fi
    printf '%s\t%s\n' "$sess" "$status"
  done
}

# Same rule as st rm: trees branched off origin/dev track it, so `@{u}..` would
# count commits merged in from other remote branches as unpushed.
unpushed_count() {
  local dir=$1
  [ -n "$(git -C "$dir" remote 2>/dev/null)" ] || { printf '0\n'; return 0; }
  git -C "$dir" rev-list --count HEAD --not --remotes 2>/dev/null || printf '0\n'
}

unpushed_log() { # dir [git log format]
  local dir=$1
  [ -n "$(git -C "$dir" remote 2>/dev/null)" ] || return 0
  git -C "$dir" log --format="${2:-%h %s}" HEAD --not --remotes 2>/dev/null || true
}

# Rows: status, repo key, repo name, branch, main|worktree, path, session,
# recency rank, then with --git the uncommitted and unpushed counts.
tree_rows() {
  local with_git=0 trees statuses mains recent
  [ "${1:-}" != --git ] || with_git=1
  trees=$(list_trees)
  [ -n "$trees" ] || return 0
  statuses=$(agent_statuses)
  mains=$(main_checkouts)
  recent="$ST_STATE/recent"
  [ -f "$recent" ] || recent=/dev/null

  printf '%s\n' "$trees" | awk -F '\t' '
    FILENAME == ARGV[1] { rank[$0] = ++r; next }
    FILENAME == ARGV[2] { if ($1 != "") status_of[$1] = $2; next }
    FILENAME == ARGV[3] { main[$0] = 1; next }
    $0 != "" {
      s = $4
      repo_name = $1
      sub(/-[^-]*$/, "", repo_name)
      n++
      print ((s in status_of) ? status_of[s] : "closed") "\t" $1 "\t" repo_name "\t" $2 "\t" \
        (($3 in main) ? "main" : "worktree") "\t" $3 "\t" s "\t" ((s in rank) ? rank[s] : 100000 + n)
    }
  ' "$recent" <(printf '%s\n' "$statuses") <(printf '%s\n' "$mains") - |
  if [ "$with_git" = 1 ]; then tree_rows_git_counts
  else awk '{ print $0 "\t\t" }'; fi
}

# Git counts are independent across worktrees. Run a bounded group together and
# print their results in input order so the picker and `st ls` keep the same rows.
tree_rows_git_counts() (
  local status repo rname branch kind path sess rank dirty unpushed
  local batch n=0 first=1 i pid pids='' failed=0
  batch=$(mktemp -d) || return 1
  trap 'rm -rf "$batch"' EXIT
  while IFS=$'\t' read -r status repo rname branch kind path sess rank; do
    n=$((n + 1))
    (
      dirty=$(git -C "$path" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
      unpushed=$(unpushed_count "$path")
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$status" "$repo" "$rname" "$branch" "$kind" "$path" "$sess" "$rank" "$dirty" "$unpushed"
    ) > "$batch/$n" &
    pids="$pids $!"
    if [ $((n - first + 1)) -eq 8 ]; then
      for pid in $pids; do wait "$pid" || failed=1; done
      for ((i=first; i<=n; i++)); do cat "$batch/$i" || failed=1; done
      first=$((n + 1))
      pids=''
    fi
  done
  for pid in $pids; do wait "$pid" || failed=1; done
  for ((i=first; i<=n; i++)); do cat "$batch/$i" || failed=1; done
  [ "$failed" = 0 ]
)

# Trees waiting on the user first, then most recently opened.
order_by_need() {
  awk -F '\t' '{ print ($1 == "input" ? 0 : 1) "\t" $8 "\t" $0 }' |
    sort -s -t $'\t' -k1,1n -k2,2n | cut -f3-
}

# Repos by their most recent tree; inside a repo, the same order as order_by_need.
order_by_repo() {
  awk -F '\t' '
    { row[NR] = $0; repo[NR] = $2; need[NR] = ($1 == "input" ? 0 : 1); rank[NR] = $8 + 0
      if (!($2 in best) || rank[NR] < best[$2]) best[$2] = rank[NR] }
    END { for (i = 1; i <= NR; i++)
            print best[repo[i]] "\t" repo[i] "\t" need[i] "\t" rank[i] "\t" row[i] }
  ' | sort -s -t $'\t' -k1,1n -k2,2 -k3,3n -k4,4n | cut -f5-
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
  exec "$(tree_shell)" -l
}

cmd_harness() {
  local repo slug marker legacy old_marker bin main branch current_tree
  main=$(main_worktree .) || true
  # Return instead of die so cmd_run still drops the pane into a shell.
  [ -n "$main" ] || { info "not inside a git repository"; return 1; }
  repo=$(repo_key "$main")
  branch=$(tree_branch .)
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
      # None of these CLIs document an exit code for "nothing to continue", so
      # any failure falls through to the shell rather than discarding the session.
      claude) claude --continue || info "Claude Code exited with an error; run 'claude' to start fresh";;
      codex) codex --no-daemon resume --last || info "Codex exited with an error; run 'codex' to start fresh";;
      openrouter) opencode --continue || info "OpenCode exited with an error; run 'opencode' to start fresh";;
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
      codex) codex --no-daemon;;
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
