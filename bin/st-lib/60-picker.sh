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

# Row fields: display, path, session. fzf shows only the display.
picker_display_rows() {
  local rows width=0 label pad mark_width
  local status repo rname branch kind path sess rank dirty unpushed
  rows=$(tree_rows "$@" | order_by_need)
  [ -n "$rows" ] || return 0
  while IFS=$'\t' read -r status repo rname branch kind path sess rank dirty unpushed; do
    label="$rname/$branch"
    [ "${#label}" -le "$width" ] || width=${#label}
  done <<ROWS
$rows
ROWS

  while IFS=$'\t' read -r status repo rname branch kind path sess rank dirty unpushed; do
    label="$rname/$branch"
    printf '%s%s%s/%s%s%s%s' "$(ui_status "$status" 11)" "$UI_REPO" "$rname" "$UI_R" "$UI_B" "$branch" "$UI_R"
    mark_width=0
    if [ "$kind" = main ]; then
      printf ' %s%s%s' "$UI_DIM" "$G_MAIN" "$UI_R"
      mark_width=2
    fi
    if [ "${dirty:-0}" -gt 0 ] || [ "${unpushed:-0}" -gt 0 ]; then
      pad=$((width - ${#label} - mark_width + 4))
      printf '%*s' "$pad" ''
      [ "${dirty:-0}" -eq 0 ] || printf '%s%s%s%s ' "$UI_WARN" "$G_DIRTY" "$dirty" "$UI_R"
      [ "${unpushed:-0}" -eq 0 ] || printf '%s%s%s%s' "$UI_DIM" "$G_AHEAD" "$unpushed" "$UI_R"
    fi
    printf '\t%s\t%s\n' "$path" "$sess"
  done <<ROWS
$rows
ROWS
}

# --git adds uncommitted and unpushed counts; that costs a git call per tree.
picker_choices() {
  if [ "${ST_PICKER_ROWS+x}" = x ]; then printf '%s\n' "$ST_PICKER_ROWS"
  else picker_display_rows "$@"; fi
  declare -F cmd_new >/dev/null || return 0
  printf '%s+%s          new tree…\t+new\t\n' "$UI_A" "$UI_R"
}

# Prints "MAJOR MINOR", or nothing when the version can't be read.
fzf_version() {
  local v major minor
  v=$(fzf --version </dev/null 2>/dev/null) || v=''
  v=${v%% *}
  major=${v%%.*}
  minor=${v#*.}
  minor=${minor%%.*}
  case $major in ''|*[!0-9]*) return 0;; esac
  case $minor in ''|*[!0-9]*) return 0;; esac
  printf '%s %s\n' "$major" "$minor"
}

version_at_least() { # "major minor" wanted_major wanted_minor
  local major=${1% *} minor=${1#* }
  [ -n "$1" ] || return 1
  [ "$major" -gt "$2" ] || { [ "$major" -eq "$2" ] && [ "$minor" -ge "$3" ]; }
}

# The design's ▌ marks only the selected row; fzf 0.66 also draws it down every row.
FZF_GUTTER=()
fzf_gutter_init() { # [version]
  local version=${1-$(fzf_version)}
  if version_at_least "$version" 0 66; then FZF_GUTTER=(--gutter=' '); fi
}

# fzf versions per https://github.com/junegunn/fzf/blob/master/CHANGELOG.md:
# transform 0.45, --gutter (default '▌') 0.66, --id-nth 0.71, every(N) 0.73.
pick_tree() {
  local rows=$1 version self need label refresh cache='' refresher='' rc=0 result qresult
  version=$(fzf_version)
  if [ -n "$version" ] && ! version_at_least "$version" 0 45; then
    ui_fail "the tree picker needs fzf 0.45 or newer (found ${version% *}.${version#* })" "brew upgrade fzf"
  fi
  self=$(printf '%q' "$(st_executable)")
  need=$(printf '%s\n' "$rows" | awk -F '\t' '$1 ~ / input/' | wc -l | tr -d ' ')
  label=' Trees '
  [ "$need" = 0 ] || label=" Trees · $G_IN $need need you "
  refresh=0
  if version_at_least "$version" 0 73; then refresh=1; fi
  mkdir -p "$ST_STATE"
  # What fzf does after ^d: _pick_rm swaps the reload for accept when you choose to open the tree.
  result=$(mktemp "$ST_STATE/.pick.XXXXXX")
  qresult=$(printf '%q' "$result")
  if [ "$refresh" = 1 ]; then
    cache=$(mktemp "$ST_STATE/.rows.XXXXXX")
    printf '%s\n' "$rows" > "$cache"
    printf 'reload(cat %q)\n' "$cache" > "$result"
  else
    printf 'reload(%s _rows)\n' "$self" > "$result"
  fi

  local -a opts=(
    --ansi --delimiter=$'\t' --with-nth=1 --height=100% --reverse --no-sort --sync
    --border=rounded --border-label="$label" --border-label-pos=3
    --prompt='Find tree › ' --pointer='▌' --info=inline-right
    --header='⏎ open  ^n new  ^d delete  ^r repos  ^/ preview  esc' --header-first
    --preview="$self _preview {3} {2}" --preview-window='right,45%,border-left,<40(hidden)'
    --expect=ctrl-n,ctrl-r
    --bind='esc:abort,ctrl-/:toggle-preview'
  )
  fzf_gutter_init "$version"
  opts+=(${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"})
  if [ "$refresh" = 0 ]; then
    opts+=("--bind=ctrl-d:execute(ST_PICK_RESULT=$qresult $self _pick_rm {2} </dev/tty >/dev/tty 2>&1)+transform(cat $qresult)")
    printf '%s\n' "$rows" | fzf "${opts[@]}" || rc=$?
    rm -f "$result"
    return "$rc"
  fi

  # --track blocks keys while a reload runs, and rows with git counts take ~50ms per tree,
  # so a background loop builds them and fzf only reads the file. After a delete,
  # _pick_rm rewrites the file so the timer cannot bring the deleted row back.
  local qcache
  qcache=$(printf '%q' "$cache")
  opts+=(
    "--bind=ctrl-d:execute(ST_PICK_CACHE=$qcache ST_PICK_RESULT=$qresult $self _pick_rm {2} </dev/tty >/dev/tty 2>&1)+transform(cat $qresult)"
    --track --id-nth=3 "--bind=every(2):reload:cat $qcache"
  )
  # First paint skips the per-tree git counts; this loop fills them in, starting at once.
  # It stops by itself if the picker dies without cleaning up (e.g. the popup is killed).
  (
    while kill -0 $$ 2>/dev/null && [ -f "$cache" ]; do
      : > "$cache.building"
      # _pick_rm removes the marker after a delete; these rows are then older than the cache.
      if "$(st_executable)" _rows --git > "$cache.next" 2>/dev/null &&
         [ -f "$cache" ] && [ -f "$cache.building" ]; then
        mv "$cache.next" "$cache"
      else
        rm -f "$cache.next"
      fi
      sleep 2
    done
  ) >/dev/null 2>&1 &
  refresher=$!
  printf '%s\n' "$rows" | fzf "${opts[@]}" || rc=$?
  kill "$refresher" 2>/dev/null || true
  rm -f "$cache" "$cache.next" "$cache.building" "$result"
  return "$rc"
}

# New tree in two steps: repo (skipped when there is only one), then branch.
pick_new() {
  local repos main
  declare -F cmd_new >/dev/null || { info "worktree module is unavailable"; return 0; }
  repos=$(known_repos)
  [ -n "$repos" ] || die "no roots known yet — run 'st new <branch>' inside a repo"
  main=$(pick_repo "$repos") || return 0
  [ -n "$main" ] || return 0
  pick_branch "$main"
}

# Known repos that still exist, the one you are standing in first.
known_repos() {
  local here root
  [ -f "$ST_REPOS" ] || return 0
  here=$(main_worktree .) || here=''
  sort -u "$ST_REPOS" | awk -v here="$here" '
    $0 == here { first = $0; next } { rest[++n] = $0 }
    END { if (first != "") print first; for (i = 1; i <= n; i++) print rest[i] }' |
    while IFS= read -r root; do [ ! -d "$root" ] || printf '%s\n' "$root"; done
}

pick_repo() {
  local repos=$1 root
  if [ "$(printf '%s\n' "$repos" | wc -l)" -eq 1 ]; then printf '%s\n' "$repos"; return 0; fi
  ui_colour_on
  fzf_gutter_init
  printf '%s\n' "$repos" | while IFS= read -r root; do
    printf '%s%s%s  %s%s%s\t%s\n' "$UI_REPO$UI_B" "$(basename "$root")" "$UI_R" "$UI_DIM" "$(ui_path "$root")" "$UI_R" "$root"
  done | fzf --ansi --delimiter=$'\t' --with-nth=1 --height=100% --reverse \
      --border=rounded --border-label=' New tree · repo ' --border-label-pos=3 \
      --prompt='Repo › ' --pointer='▌' --header='⏎ next  esc' --header-first \
      --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"} | cut -f2
}

pick_branch() {
  local main=$1 base='' bare=0 out query key sel kind name rows self header
  self=$(printf '%q' "$(st_executable)")
  ui_colour_on
  fzf_gutter_init
  rows=$(mktemp)
  branch_rows "$main" > "$rows"
  while :; do
    header="⏎ create  tab base: ${base:-default}  ^b bare: $([ "$bare" = 1 ] && printf on || printf off)  esc"
    out=$(cat "$rows" | fzf --ansi --delimiter=$'\t' --with-nth=1 --height=100% --reverse --no-sort \
      --border=rounded --border-label=" New tree · $(basename "$main") " --border-label-pos=3 \
      --prompt='Branch › ' --pointer='▌' --header="$header" --header-first \
      --print-query --expect=tab,ctrl-b --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"} \
      "--bind=change:reload($self _branch_rows $(printf '%q' "$rows") {q})") || { rm -f "$rows"; return 0; }
    query=$(printf '%s\n' "$out" | sed -n 1p)
    key=$(printf '%s\n' "$out" | sed -n 2p)
    sel=$(printf '%s\n' "$out" | sed -n 3p)
    kind=$(printf '%s' "$sel" | cut -f2)
    name=$(printf '%s' "$sel" | cut -f3)
    case $key in
      ctrl-b) bare=$((1 - bare)); continue;;
      tab) base=$(cut -f3 "$rows" | fzf --height=100% --reverse --border=rounded \
             --border-label=' Base branch ' --prompt='Base › ' --pointer='▌' \
             --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"}) || true
           continue;;
    esac
    [ -n "$sel" ] && [ "$kind" != new ] || { name=$query; kind=new; }
    [ -n "$name" ] || continue
    if [ "$kind" = remote ]; then
      [ -n "$base" ] || base=$name
      name=${name#*/}
    fi
    if ! git check-ref-format --branch "$name" >/dev/null 2>&1; then
      ui_warn "bad name" "$name is not a valid branch name"
      sleep 1.5
      continue
    fi
    rm -f "$rows"
    cmd_new "$name" --repo "$main" ${base:+--from "$base"} $([ "$bare" = 0 ] || printf -- --bare)
    return
  done
}

# Fields: display, local|remote|new, name. Remote rows only for branches not checked out locally.
branch_rows() {
  local main=$1
  git -C "$main" for-each-ref --format='%(refname)' refs/heads refs/remotes 2>/dev/null |
    awk -v dim="$UI_DIM" -v r="$UI_R" '
      /^refs\/remotes\/[^\/]+\/HEAD$/ { next }
      sub(/^refs\/heads\//, "") { local[$0] = 1; print $0 "\tlocal\t" $0; next }
      sub(/^refs\/remotes\//, "") { remote[++n] = $0 }
      END {
        for (i = 1; i <= n; i++) {
          short = remote[i]; sub(/^[^\/]+\//, "", short)
          if (!(short in local)) print dim remote[i] r "\tremote\t" remote[i]
        }
      }'
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

  local rows sel dir sess key live_dir
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
    ui_colour_on
    sel=$(pick_tree "$(picker_choices)") || return 0
    key=${sel%%$'\n'*}
    sel=${sel#*$'\n'}
    case $key in
      ctrl-n) pick_new; return;;
      ctrl-r) cmd_roots; return;;
    esac
  fi

  dir=$(printf '%s' "$sel" | cut -f2)
  [ "$dir" != +new ] || { pick_new; return; }
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

cmd_rows() {
  ui_colour_on
  picker_choices "$@"
}

# The branch list with a "new branch" row on top while the typed name is not taken.
cmd_branch_rows() {
  local rows=$1 query=${2:-}
  ui_colour_on
  if [ -n "$query" ] && ! cut -f3 "$rows" | grep -qxF -- "$query"; then
    printf '%s+%s new branch %s%s%s\tnew\t%s\n' "$UI_A" "$UI_R" "$UI_B" "$query" "$UI_R" "$query"
  fi
  cat "$rows"
}

cmd_preview() {
  local sess=${1:-} dir=${2:-} status changes unpushed
  ui_colour_on
  if [ -z "$sess" ]; then
    printf '\n  %sstart a tree in any known repo%s\n' "$UI_DIM" "$UI_R"
    return 0
  fi
  status=$(agent_status "$sess")
  printf '%s%s%s\n' "$UI_DIM" "$(ui_path "$dir")" "$UI_R"
  case $status in
    input) printf '%s%s waiting for you%s\n' "$UI_IN" "$G_IN" "$UI_R";;
    running) printf '%s%s working%s\n' "$UI_RUN" "$G_RUN" "$UI_R";;
    done) printf '%s%s agent exited%s\n' "$UI_OK" "$G_DONE" "$UI_R";;
    *) printf '%s%s session closed%s\n' "$UI_DIM" "$G_OFF" "$UI_R";;
  esac

  changes=$(git -C "$dir" status --porcelain 2>/dev/null || true)
  if [ -n "$changes" ]; then
    printf '\n%s─ changes %s ─%s\n' "$UI_DIM" "$(printf '%s\n' "$changes" | wc -l | tr -d ' ')" "$UI_R"
    printf '%s\n' "$changes" | head -8 |
      awk -v c="$UI_WARN" -v r="$UI_R" '{ print "  " c substr($0, 1, 2) r substr($0, 3) }'
  fi

  unpushed=$(unpushed_log "$dir")
  if [ -n "$unpushed" ]; then
    printf '\n%s─ unpushed %s ─%s\n' "$UI_DIM" "$(printf '%s\n' "$unpushed" | wc -l | tr -d ' ')" "$UI_R"
    printf '%s\n' "$unpushed" | head -5 |
      awk -v c="$UI_DIM" -v r="$UI_R" '{ hash = $1; sub(/^[^ ]* /, ""); print "  " c hash r " " $0 }'
  fi

  [ "$status" != closed ] || return 0
  printf '\n%s─ agent ─%s\n' "$UI_DIM" "$UI_R"
  # awk, not sed: macOS sed rejects the multibyte bar outside a UTF-8 locale.
  agent_tail "$sess" 10 | awk -v c="$UI_DIM" -v r="$UI_R" '{ print c "│" r " " $0 }'
}

# Runs inside the picker via execute(), so refusals wait for a key instead of flashing.
# Work that exists only in the tree gets a choice instead of a dead end.
cmd_pick_rm() {
  local dir=${1:-} rc=0
  case $dir in ''|+new) return 0;; esac
  declare -F remove_tree_at >/dev/null || { info "remove module is unavailable"; ui_hold; return 1; }
  ( remove_tree_at "$dir" ) || rc=$?
  if [ "$rc" = 3 ] && [ -t 0 ]; then
    ui_keys ⏎ "open tree" ^f "force delete" esc back
    case $(ui_key) in
      '') [ -z "${ST_PICK_RESULT:-}" ] || printf 'accept\n' > "$ST_PICK_RESULT"; return 0;;
      $'\x06') rc=0; ( remove_tree_at "$dir" --force ) || rc=$?;;
      *) return 0;;
    esac
  fi
  case $rc in 0) ;; 4) return 0;; *) ui_hold; return 1;; esac
  [ -z "${ST_PICK_CACHE:-}" ] || [ ! -f "$ST_PICK_CACHE" ] || {
    rm -f "$ST_PICK_CACHE.building"
    ui_colour_on
    picker_choices > "$ST_PICK_CACHE"
  }
}

cmd_roots() {
  local popup_client='' self
  case ${1:-} in
    --popup) [ $# -ge 2 ] || die "usage: st roots --popup <tmux-client>"; popup_client=$2;;
    '') ;;
    *) die "usage: st roots";;
  esac
  [ -f "$ST_REPOS" ] || die "no roots known yet — run 'st new <branch>' inside a repo"

  if [ -n "$popup_client" ]; then
    self=$(st_executable)
    tmux display-popup -c "$popup_client" -d "$PWD" -EE -w 72% -h 60% "$(printf '%q roots' "$self")"
    return
  fi

  command -v fzf >/dev/null || die "fzf is required for the root picker"
  pick_new
}

st_register_command 'start' cmd_start ''
st_register_command 'open' cmd_open 'open this tree, or the most recent one'
st_register_command 'go' cmd_go 'pick a tree and switch to it' 'go [query]'
st_register_command 'resume' cmd_go 'reopen a tree and resume its agent' 'resume [query]'
st_register_command 'roots' cmd_roots 'pick a known repo and create a tree in it'
st_register_command '_rows' cmd_rows ''
st_register_command '_preview' cmd_preview ''
st_register_command '_branch_rows' cmd_branch_rows ''
st_register_command '_pick_rm' cmd_pick_rm ''
