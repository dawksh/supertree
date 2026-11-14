# shellcheck shell=bash
# supertree worktrees module

cmd_new() {
  local branch="" base="" bare=0 repo_arg=""
  while [ $# -gt 0 ]; do
    case $1 in
      --from) base=$2; shift 2;;
      --repo) repo_arg=$2; shift 2;;
      --bare) bare=1; shift;;
      -*) die "unknown flag: $1";;
      *) branch=$1; shift;;
    esac
  done
  [ -n "$branch" ] || die "usage: st new <branch> [--from <base>] [--bare]"

  local main repo slug dir sess idx existing stale
  main=$(require_repo "${repo_arg:-.}")
  repo=$(repo_key "$main")
  slug=$(branch_key "$branch")
  dir=$(short_tree_dir "$repo" "$slug")
  [ "$bare" = 1 ] || prepare_repo_config "$main"
  register_repo "$main"

  if [ -n "$base" ] && git -C "$main" show-ref --verify --quiet "refs/heads/$branch"; then
    info "$branch already exists; ignoring --from $base"
  fi
  existing=$(branch_worktree "$main" "$branch" || true)
  if [ -n "$existing" ]; then
    [ "$existing" != "$main" ] || die "$branch is the main checkout ($main); use 'st go'"
    managed_tree "$main" "$branch" "$existing" ||
      die "$branch is already checked out outside supertree: $existing"
    dir=$existing
    info "worktree already exists: $(basename "$main")/$branch"
  else
    # Clears only that record; git refuses to add the branch while it is registered.
    # https://git-scm.com/docs/git-worktree#Documentation/git-worktree.txt-remove
    while IFS= read -r stale; do
      [ -n "$stale" ] || continue
      git -C "$main" worktree remove "$stale" || die "cannot clear missing worktree: $stale"
      info "cleared missing worktree: $stale"
    done <<< "$(stale_branch_worktrees "$main" "$branch")"
    [ ! -e "$dir" ] && [ ! -L "$dir" ] || die "path already exists: $dir"
    mkdir -p "$(dirname "$dir")"
    if git -C "$main" show-ref --verify --quiet "refs/heads/$branch"; then
      git -C "$main" worktree add -q "$dir" "$branch"
    else
      base=$(pre_create "$main" "$branch" "$base") || die "pre-create hook failed; no worktree created"
      case $base in -*) die "invalid base: $base";; esac
      [ -z "$base" ] || git -C "$main" rev-parse --verify --quiet "$base^{commit}" >/dev/null ||
        die "unknown base: $base; no worktree created"
      git -C "$main" worktree add -q -b "$branch" "$dir" ${base:+"$base"}
    fi
  fi

  if [ "$dir" -ef "$(tree_dir "$(basename "$main")" "$(slugify "$branch")")" ] &&
     [ -f "$ST_STATE/idx/$(basename "$main")/$(slugify "$branch")" ] &&
     [ ! -f "$ST_STATE/idx/$repo/$slug" ]; then
    mkdir -p "$ST_STATE/idx/$repo"
    cp "$ST_STATE/idx/$(basename "$main")/$(slugify "$branch")" "$ST_STATE/idx/$repo/$slug"
  fi
  idx=$(tree_index "$repo" "$slug" "$(basename "$main")")
  if [ "$bare" = 0 ]; then
    bootstrap "$main" "$dir" "$repo" "$branch" "$idx"
    sess=$(tree_session "$repo" "$branch")
    [ -n "$sess" ] || die "cannot find a session name for $branch"
    build_session "$sess" "$dir"
    attach "$sess"
  else
    info "bare: skipped deps, env and agent"
    printf '%s\n' "$dir"
  fi
}

cmd_trust() {
  local main file show=0 snapshot
  case ${1:-} in --show) show=1; shift;; esac
  [ $# -eq 0 ] || die "usage: st trust [--show]"
  main=$(require_repo .)
  file="$main/.supertree"
  [ -f "$file" ] || die "no .supertree in $main"
  if [ "$show" = 1 ]; then trust_view "$main" "$file"; return 0; fi
  # Trust exactly the bytes fingerprinted, even if the file changes meanwhile.
  snapshot=$(mktemp)
  cp "$file" "$snapshot"
  trust_config "$main" "$snapshot"
  rm -f "$snapshot"
  ui_ok trusted "$(ui_path "$file"); changes will require trusting it again"
}

cmd_ls() {
  local rows
  rows=$(tree_rows --git | order_by_repo)
  if [ ! -t 1 ]; then
    [ -z "$rows" ] || printf '%s\n' "$rows" | awk -F '\t' '{
      open = $1 != "closed"
      print $3 "/" $4 "\t" (open ? "open" : "closed") "\t" (open ? $1 : "-") "\t" \
        ($9 > 0 ? "modified" : "clean") "\t" $5
    }'
    return 0
  fi
  [ -n "$rows" ] || { ui_info "no trees yet — run st new <branch> inside a repo"; return 0; }
  ls_grid "$rows"
}

ls_grid() {
  local rows=$1 width=14 group='' main mark_width changes changes_color changes_width
  local status repo rname branch kind path sess rank dirty unpushed
  local total=0 need=0 run=0 finished=0 closed=0
  while IFS=$'\t' read -r status repo rname branch kind path sess rank dirty unpushed; do
    [ $(( ${#branch} + 4 )) -le "$width" ] || width=$(( ${#branch} + 4 ))
  done <<ROWS
$rows
ROWS

  while IFS=$'\t' read -r status repo rname branch kind path sess rank dirty unpushed; do
    if [ "$repo" != "$group" ]; then
      group=$repo
      main=$(main_worktree "$path")
      printf '  %s%s%s%s  %s%s\n' "$UI_REPO$UI_B" "$rname" "$UI_R" "$UI_DIM" "$(ui_path "${main:-$path}")" "$UI_R"
    fi
    printf '    %s%s%s%s' "$(ui_status "$status" 11)" "$UI_B" "$branch" "$UI_R"
    mark_width=0
    if [ "$kind" = main ]; then
      printf ' %s%s%s' "$UI_DIM" "$G_MAIN" "$UI_R"
      mark_width=2
    fi
    printf '%*s' $(( width - ${#branch} - mark_width )) ''
    if [ "$dirty" -gt 0 ]; then
      changes="$G_DIRTY$dirty" changes_color=$UI_WARN changes_width=$((1 + ${#dirty}))
    else
      changes=clean changes_color=$UI_DIM changes_width=5
    fi
    if [ "$unpushed" -eq 0 ]; then
      printf '%s%s%s\n' "$changes_color" "$changes" "$UI_R"
    else
      printf '%s%s%s%*s%s%s%s%s\n' "$changes_color" "$changes" "$UI_R" $((6 - changes_width)) '' \
        "$UI_WARN" "$G_AHEAD" "$unpushed" "$UI_R"
    fi
    total=$((total + 1))
    case $status in
      input) need=$((need + 1));;
      running) run=$((run + 1));;
      done) finished=$((finished + 1));;
      *) closed=$((closed + 1));;
    esac
  done <<ROWS
$rows
ROWS

  printf '\n  %s%s trees%s' "$UI_DIM" "$total" "$UI_R"
  [ "$need" = 0 ] || printf '%s · %s%s %s need you%s' "$UI_DIM" "$UI_IN" "$G_IN" "$need" "$UI_R"
  [ "$run" = 0 ] || printf '%s · %s%s %s%s' "$UI_DIM" "$UI_RUN" "$G_RUN" "$run" "$UI_R"
  [ "$finished" = 0 ] || printf '%s · %s%s %s' "$UI_DIM" "$UI_R" "$G_DONE" "$finished"
  [ "$closed" = 0 ] || printf '%s · %s closed%s' "$UI_DIM" "$closed" "$UI_R"
  printf '\n'
}

cmd_status() {
  local s all trees
  trees=$(list_trees)
  all=$(printf '%s\n' "$trees" | cut -f4)
  if [ $# -gt 0 ]; then
    s=$(resolve_session "$1" "$trees") || ui_fail "no tree matching: $1" "st ls  ·  M-w"
  elif [ -n "${TMUX:-}" ]; then
    s=$(current_session)
    case $'\n'"$all"$'\n' in
      *$'\n'"$s"$'\n'*) ;;
      *) die "$s is not a supertree session";;
    esac
  else
    die "usage: st status <tree>  (omit tree inside tmux)"
  fi
  if [ -t 1 ]; then
    printf '%s  %s\n' "$(label_for_session "$s" "$trees")" "$(ui_status "$(agent_status "$s")")"
    return 0
  fi
  printf '%s\t%s\n' "$(label_for_session "$s" "$trees")" "$(agent_status "$s")"
}

cmd_rm() {
  local branch="" force=0 repo_arg=""
  while [ $# -gt 0 ]; do
    case $1 in
      --force|-f) force=1; shift;;
      --repo) repo_arg=$2; shift 2;;
      *) branch=$1; shift;;
    esac
  done
  [ -n "$branch" ] || die "usage: st rm <branch> [--force]"

  local main repo slug dir sess report current old_sess current_sess='' label fate
  main=$(require_repo "${repo_arg:-.}")
  repo=$(repo_key "$main")
  slug=$(branch_key "$branch")
  dir=$(branch_worktree "$main" "$branch" || true)
  [ -n "$dir" ] && managed_tree "$main" "$branch" "$dir" || die "no managed worktree for $branch"
  sess=$(tree_session "$repo" "$branch")
  [ -d "$dir" ] || die "no worktree for $branch at $dir"
  [ "$dir" != "$main" ] || die "cannot remove the main worktree"
  old_sess=$(sess_name "$(basename "$main")" "$(slugify "$branch")")
  if [ "$old_sess" != "$sess" ] &&
     [ "$dir" -ef "$(tree_dir "$(basename "$main")" "$(slugify "$branch")")" ] &&
     tmux has-session -t "=$old_sess" 2>/dev/null; then
    die "an older session for $branch is still open; close it before removing the tree"
  fi

  ! git_op_in_progress "$dir" || die "$branch is mid rebase or bisect; finish or abort it first"
  label="$(basename "$main")/$branch"
  report=$(tree_dirty_report "$dir")
  if [ "$force" = 0 ] && [ -n "$report" ]; then
    if [ "$UI_ROWS" = 1 ]; then
      ui_title "$label has work that exists only here"
      show_unsaved_work "$dir"
      printf '\n' >&2
    else
      printf '%s\n' "$report" >&2
    fi
    # Exit 3 tells the picker this refusal can be overridden with force.
    ui_fail "refusing to remove $branch — commit/push, or pass --force" "st rm $branch --force" 3
  fi

  fate=keep
  if branch_merged "$main" "$branch"; then fate=delete; fi
  if [ "$UI_ROWS" = 1 ]; then
    ui_title "Remove $label"
    ui_info session close
    ui_info checkout "remove $(ui_path "$dir")"
    if [ "$fate" = delete ]; then ui_info branch "delete (merged)"
    else ui_info branch "keep (not merged)"; fi
    if [ "$force" = 1 ] && [ -n "$report" ]; then
      show_unsaved_work "$dir"
      ui_warn lost "these changes and commits go away for good"
      ui_type_confirm "$branch" || ui_abort
    else
      ui_ask 'proceed?' n || ui_abort
    fi
  else
    printf 'will remove:\n  tree      %s\n  session   close\n  checkout  remove\n  branch    %s\n' "$label" "$fate" >&2
    ui_ask 'proceed?' n || die "aborted"
  fi

  current=$(branch_worktree "$main" "$branch" || true)
  [ "$current" = "$dir" ] || die "worktree for $branch changed; refusing to remove $dir"

  [ -z "${TMUX:-}" ] || current_sess=$(current_session)
  if [ "$force" = 1 ]; then git -C "$main" worktree remove --force "$dir"
  else git -C "$main" worktree remove "$dir"; fi
  if [ -n "$sess" ]; then
    evacuate_clients "$sess"
    # Killing our own session kills this process, so close it only after cleanup.
    if [ "$sess" != "$current_sess" ]; then tmux kill-session -t "=$sess" 2>/dev/null || true; fi
  fi
  git -C "$main" branch -d "$branch" >/dev/null 2>&1 || ui_warn branch "$branch kept (not merged)"
  clear_tree_state "$repo" "$slug" "$sess"
  ui_ok removed "$(basename "$main")/$branch"
  if [ -n "$sess" ] && [ "$sess" = "$current_sess" ]; then tmux kill-session -t "=$sess" 2>/dev/null || true; fi
}

# Removing a worktree drops its rebase state and reflog, losing rebased commits.
git_op_in_progress() {
  local git_dir
  git_dir=$(git -C "$1" rev-parse --absolute-git-dir 2>/dev/null) || return 1
  [ -e "$git_dir/rebase-merge" ] || [ -e "$git_dir/rebase-apply" ] || [ -e "$git_dir/BISECT_START" ]
}

# Picker and sidebar rows carry a tree's path, not its branch.
remove_tree_at() { # dir [--force]
  local dir=$1 branch main
  [ -d "$dir" ] || ui_fail "already removed: $(ui_path "$dir")"
  main=$(main_worktree "$dir")
  branch=$(tree_branch "$dir")
  [ -n "$branch" ] ||
    ui_fail "$(basename "$dir") is a detached checkout, not a tree st can delete" "git -C $(ui_path "$main") worktree remove $(ui_path "$dir")"
  [ "$dir" != "$main" ] ||
    ui_fail "$(basename "$main")/$branch $G_MAIN is the main checkout and can't be deleted" "st down $branch  to close its session"
  cmd_rm "$branch" --repo "$main" ${2:+"$2"}
}

# Same test as `git branch -d`: merged into its upstream, or into HEAD when it
# has none. https://git-scm.com/docs/git-branch#Documentation/git-branch.txt--d
branch_merged() {
  local main=$1 branch=$2 target
  target=$(git -C "$main" rev-parse --abbrev-ref "$branch@{u}" 2>/dev/null) || target=HEAD
  git -C "$main" merge-base --is-ancestor "refs/heads/$branch" "$target" 2>/dev/null
}

show_unsaved_work() {
  local dir=$1 dirty unpushed n
  dirty=$(git -C "$dir" status --porcelain 2>/dev/null || true)
  unpushed=$(unpushed_log "$dir")
  if [ -n "$dirty" ]; then
    n=$(printf '%s\n' "$dirty" | wc -l | tr -d ' ')
    ui_warn uncommitted "$n file$([ "$n" = 1 ] || printf s)"
    printf '%s\n' "$dirty" | ui_list 5
  fi
  if [ -n "$unpushed" ]; then
    n=$(printf '%s\n' "$unpushed" | wc -l | tr -d ' ')
    ui_warn unpushed "$n commit$([ "$n" = 1 ] || printf s)"
    printf '%s\n' "$unpushed" | ui_list 5
  fi
}

tree_dirty_report() {
  local dir=$1 dirty unpushed
  dirty=$(git -C "$dir" status --porcelain 2>/dev/null || true)
  unpushed=$(unpushed_log "$dir" %s)
  [ -z "$dirty" ] || printf 'uncommitted changes:\n%s\n' "$dirty"
  [ -z "$unpushed" ] || printf 'unpushed commits:\n%s\n' "$unpushed"
}

clear_tree_state() {
  local repo=$1 slug=$2 sess=$3
  rm -f "$ST_STATE/idx/$repo/$slug" "$ST_STATE/seen/$repo/$slug" "$ST_STATE/seen/$repo/$slug".*
  [ ! -f "$ST_STATE/recent" ] || rewrite_recent "$sess" 0
  if declare -F sidebar_forget >/dev/null; then sidebar_forget "$sess"; fi
}

cmd_remove_all() {
  local force=0 root dir main branch sess repo slug current other found tmp j trees
  local current_sess='' close_current=0
  local -a dirs=() mains=() branches=() sessions=() kinds=()
  case ${1:-} in --force|-f) force=1; shift;; esac
  [ $# -eq 0 ] || die "usage: st remove all [--force]"
  [ -d "$ST_WORKTREE_ROOT" ] || { info "no worktrees under $ST_WORKTREE_ROOT"; return 0; }
  root=$(cd "$ST_WORKTREE_ROOT" && pwd -P)
  [ "$root" != / ] && [ "$root" != "$HOME" ] || die "unsafe worktree root: $root"

  trees=$(list_trees)
  for dir in "$root"/* "$root"/*/*; do
    [ -d "$dir" ] && [ ! -L "$dir" ] && [ ! -L "$dir/.git" ] &&
      { [ -f "$dir/.git" ] || [ -d "$dir/.git" ]; } || continue
    [ "$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null || true)" = "$dir" ] || continue
    main=$(main_worktree "$dir")
    [ -n "$main" ] || continue
    branch=$(tree_branch "$dir")
    if [ "$main" = "$dir" ]; then
      # Do not list a nested checkout twice when its parent is a repository.
      case $dir in "$root"/*/*)
        [ -e "$(dirname "$dir")/.git" ] && continue;;
      esac
      sess=''
      if [ -n "$branch" ] && grep -qxF "$dir" "$ST_REPOS" 2>/dev/null; then
        sess=$(tree_session "$(repo_key "$dir")" "$branch" "$trees")
      fi
      dirs+=("$dir"); mains+=("$main"); branches+=("$branch")
      sessions+=("$sess"); kinds+=(repo)
    else
      if [ -n "$branch" ]; then
        [ "$(branch_worktree "$main" "$branch" || true)" = "$dir" ] ||
          die "worktree changed while listing: $dir"
      fi
      repo=$(repo_key "$main")
      sess=$(tree_session "$repo" "${branch:-(detached)}" "$trees")
      dirs+=("$dir"); mains+=("$main"); branches+=("$branch")
      sessions+=("$sess"); kinds+=(worktree)
    fi
  done
  [ ${#dirs[@]} -gt 0 ] || { info "no Git checkouts under $root"; return 0; }

  printf 'will remove from %s:\n' "$root" >&2
  local i
  for ((i=0; i<${#dirs[@]}; i++)); do
    if [ "${kinds[i]}" = worktree ]; then
      printf '  %-10s %s/%s\n' worktree "$(basename "${mains[i]}")" "${branches[i]:-(detached)}" >&2
    else
      printf '  %-10s %s\n' repository "${dirs[i]}" >&2
    fi
  done
  for ((i=0; i<${#dirs[@]}; i++)); do
    dir=${dirs[i]}
    if [ "${kinds[i]}" = repo ] && [ "$force" = 0 ]; then
      die "standalone repositories require --force (their history would be deleted)"
    fi
    if [ "${kinds[i]}" = repo ]; then
      while IFS= read -r other; do
        [ "$other" = "$dir" ] && continue
        found=0
        for ((j=0; j<${#dirs[@]}; j++)); do
          [ "${dirs[j]}" = "$other" ] && [ "${kinds[j]}" = worktree ] && found=1
        done
        [ "$found" = 1 ] || die "refusing to delete $dir; linked worktree outside removal list: $other"
      done < <(git -C "$dir" worktree list --porcelain | sed -n 's/^worktree //p')
    fi
    ! git_op_in_progress "$dir" ||
      die "$(basename "${mains[i]}")/${branches[i]:-(detached)} is mid rebase or bisect; finish or abort it first"
    if [ "$force" = 0 ] && [ -n "$(tree_dirty_report "$dir")" ]; then
      die "refusing to remove $(basename "${mains[i]}")/${branches[i]:-(detached)} with uncommitted or unpushed work; pass --force"
    fi
  done
  ui_ask 'proceed?' n || die "aborted"
  [ -z "${TMUX:-}" ] || current_sess=$(current_session)

  # Linked worktrees must go first when a standalone repo under the root owns
  # one of them. Revalidate each candidate before touching it.
  local kind
  for kind in worktree repo; do
    for ((i=0; i<${#dirs[@]}; i++)); do
      [ "${kinds[i]}" = "$kind" ] || continue
      dir=${dirs[i]}; main=${mains[i]}; branch=${branches[i]}; sess=${sessions[i]}
      [ -d "$dir" ] && [ ! -L "$dir" ] &&
        [ "$(main_worktree "$dir")" = "$main" ] ||
        die "checkout changed; refusing to remove $dir"
      if [ "$kind" = worktree ]; then
        repo=$(repo_key "$main")
        if [ -n "$branch" ]; then
          current=$(branch_worktree "$main" "$branch" || true)
          [ "$current" = "$dir" ] || die "worktree changed; refusing to remove $dir"
        fi
        if [ "$force" = 1 ]; then git -C "$main" worktree remove --force "$dir"
        else git -C "$main" worktree remove "$dir"; fi
        if [ -n "$sess" ]; then
          evacuate_clients "$sess"
          if [ "$sess" = "$current_sess" ]; then close_current=1
          else tmux kill-session -t "=$sess" 2>/dev/null || true; fi
        fi
        [ -z "$branch" ] || git -C "$main" branch -d "$branch" >/dev/null 2>&1 ||
          ui_warn branch "$branch kept (not merged)"
        clear_tree_state "$repo" "$(branch_key "${branch:-(detached)}")" "$sess"
      else
        rm -rf -- "$dir"
        if [ -n "$sess" ]; then
          evacuate_clients "$sess"
          if [ "$sess" = "$current_sess" ]; then close_current=1
          else tmux kill-session -t "=$sess" 2>/dev/null || true; fi
        fi
        if [ -f "$ST_REPOS" ]; then
          tmp=$(mktemp "$ST_STATE/.repos.XXXXXX")
          grep -vxF -- "$dir" "$ST_REPOS" > "$tmp" || true
          mv "$tmp" "$ST_REPOS"
        fi
      fi
      if [ "$kind" = worktree ]; then
        ui_ok removed "$(basename "$main")/${branch:-(detached)}"
      else
        ui_ok removed "$dir"
      fi
    done
  done
  if [ "$close_current" = 1 ]; then
    tmux kill-session -t "=$current_sess" 2>/dev/null || true
  fi
}

cmd_remove() {
  [ "${1:-}" = all ] || die "usage: st remove all [--force]"
  shift
  cmd_remove_all "$@"
}


st_register_command 'new' cmd_new 'create a worktree and open its session' 'new <branch> [--from <base>] [--bare]'
st_register_command 'trust' cmd_trust 'trust a reviewed .supertree config (--show: what changed)' 'trust [--show]'
st_register_command 'ls' cmd_ls 'every tree grouped by repo, waiting ones first'
st_register_command 'status' cmd_status 'show agent status' 'status [tree]'
st_register_command 'rm' cmd_rm 'remove a worktree and its session' 'rm <branch> [--force]'
st_register_command 'remove' cmd_remove 'remove Git checkouts under the worktree root' 'remove all [--force]'
