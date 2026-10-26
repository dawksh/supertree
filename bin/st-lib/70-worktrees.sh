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

  local main repo slug dir sess idx existing
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
  [ $# -eq 0 ] || die "usage: st trust"
  local main fingerprint file
  main=$(require_repo .)
  file="$main/.supertree"
  [ -f "$file" ] || die "no .supertree in $main"
  fingerprint=$(config_fingerprint "$file")
  mkdir -p "$ST_STATE/trust"
  ( umask 077; printf '%s\n' "$fingerprint" > "$ST_STATE/trust/$(repo_key "$main")" )
  info "trusted $file; changes will require trusting it again"
}

cmd_ls() {
  local repo branch path s live changes agent kind repos statuses
  repos=$(main_checkouts)
  statuses=$(agent_statuses)
  list_trees | while IFS=$'\t' read -r repo branch path s; do
    agent=$(printf '%s\n' "$statuses" | awk -F '\t' -v s="$s" '$1 == s { print $2; exit }')
    if [ -n "$agent" ]; then live='open'; else live='closed'; agent='-'; fi
    if [ -n "$(git -C "$path" status --porcelain 2>/dev/null)" ]; then changes='modified'; else changes='clean'; fi
    case $'\n'"$repos"$'\n' in
      *$'\n'"$path"$'\n'*) kind='main';;
      *) kind='worktree';;
    esac
    printf '%s\t%s\t%s\t%s\t%s\n' "$(tree_label "$repo" "$branch")" "$live" "$agent" "$changes" "$kind"
  done | awk -F '\t' '
    { for (i = 1; i <= 5; i++) { cell[NR,i] = $i; if (length($i) > width[i]) width[i] = length($i) } }
    END {
      title[1] = "TREE"; title[2] = "SESSION"; title[3] = "AGENT"
      title[4] = "CHANGES"; title[5] = "TYPE"
      for (i = 1; i <= 5; i++) if (length(title[i]) > width[i]) width[i] = length(title[i])
      for (row = 0; row <= NR; row++) {
        for (i = 1; i <= 5; i++) {
          value = row ? cell[row,i] : title[i]
          if (i == 5) printf "%s\n", value
          else printf "%-*s  ", width[i], value
        }
        if (row == 0) {
          for (i = 1; i <= 5; i++) {
            for (j = 0; j < width[i]; j++) printf "-"
            printf "%s", (i == 5 ? "\n" : "  ")
          }
        }
      }
    }'
}

cmd_status() {
  local s all trees
  trees=$(list_trees)
  all=$(printf '%s\n' "$trees" | cut -f4)
  if [ $# -gt 0 ]; then
    s=$(resolve_session "$1" "$trees") || die "no tree matching: $1"
  elif [ -n "${TMUX:-}" ]; then
    s=$(current_session)
    case $'\n'"$all"$'\n' in
      *$'\n'"$s"$'\n'*) ;;
      *) die "$s is not a supertree session";;
    esac
  else
    die "usage: st status <tree>  (omit tree inside tmux)"
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

  local main repo slug dir sess report current old_sess current_sess=''
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
  report=$(tree_dirty_report "$dir")
  if [ "$force" = 0 ] && [ -n "$report" ]; then
    printf '%s\n' "$report" >&2
    die "refusing to remove $branch — commit/push, or pass --force"
  fi

  printf 'will remove:\n  tree      %s/%s\n  session   close\n  checkout  remove\n  branch    delete if merged\n' "$(basename "$main")" "$branch" >&2
  printf 'proceed? [y/N] ' >&2; read -r ans
  case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac

  current=$(branch_worktree "$main" "$branch" || true)
  [ "$current" = "$dir" ] || die "worktree for $branch changed; refusing to remove $dir"

  [ -z "${TMUX:-}" ] || current_sess=$(current_session)
  if [ "$force" = 1 ]; then git -C "$main" worktree remove --force "$dir"
  else git -C "$main" worktree remove "$dir"; fi
  evacuate_clients "$sess"
  # Killing our own session kills this process, so close it only after cleanup.
  if [ "$sess" != "$current_sess" ]; then tmux kill-session -t "=$sess" 2>/dev/null || true; fi
  git -C "$main" branch -d "$branch" >/dev/null 2>&1 || info "branch $branch kept (not merged)"
  clear_tree_state "$repo" "$slug" "$sess"
  info "removed $(basename "$main")/$branch"
  if [ "$sess" = "$current_sess" ]; then tmux kill-session -t "=$sess" 2>/dev/null || true; fi
}

# Removing a worktree drops its rebase state and reflog, losing rebased commits.
git_op_in_progress() {
  local git_dir
  git_dir=$(git -C "$1" rev-parse --absolute-git-dir 2>/dev/null) || return 1
  [ -e "$git_dir/rebase-merge" ] || [ -e "$git_dir/rebase-apply" ] || [ -e "$git_dir/BISECT_START" ]
}

# Picker and sidebar rows carry a tree's path, not its branch.
remove_tree_at() {
  local dir=$1 branch main
  [ -d "$dir" ] || die "already removed: $dir"
  branch=$(tree_branch "$dir")
  [ -n "$branch" ] || die "cannot delete a detached worktree"
  main=$(main_worktree "$dir")
  [ "$dir" != "$main" ] || die "cannot delete the main worktree"
  cmd_rm "$branch" --repo "$main"
}

tree_dirty_report() {
  local dir=$1 dirty unpushed=''
  dirty=$(git -C "$dir" status --porcelain 2>/dev/null || true)
  # Not `@{u}..`: trees branched off origin/dev track it, so commits merged in from any remote branch look unpushed.
  if [ -n "$(git -C "$dir" remote)" ]; then
    unpushed=$(git -C "$dir" log --format=%s HEAD --not --remotes 2>/dev/null || true)
  fi
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
  printf 'proceed? [y/N] ' >&2; read -r ans
  case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac
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
          info "branch $branch kept (not merged)"
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
        info "removed $(basename "$main")/${branch:-(detached)}"
      else
        info "removed $dir"
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
st_register_command 'trust' cmd_trust 'trust a reviewed .supertree config'
st_register_command 'ls' cmd_ls 'show every tree in a readable grid'
st_register_command 'status' cmd_status 'show agent status' 'status [tree]'
st_register_command 'rm' cmd_rm 'remove a worktree and its session' 'rm <branch> [--force]'
st_register_command 'remove' cmd_remove 'remove Git checkouts under the worktree root' 'remove all [--force]'
