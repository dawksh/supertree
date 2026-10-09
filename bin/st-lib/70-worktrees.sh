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
  dir=$(tree_dir "$repo" "$slug")
  [ "$bare" = 1 ] || prepare_repo_config "$main"
  register_repo "$main"

  existing=$(branch_worktree "$main" "$branch" || true)
  if [ -n "$existing" ]; then
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
    attach "$sess" "$dir"
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
  local repo branch path s live changes agent kind root mains=$'\n'
  if [ -f "$ST_REPOS" ]; then
    while IFS= read -r root; do
      [ -d "$root" ] || continue
      root=$(cd "$root" && pwd -P)
      mains="$mains$root"$'\n'
    done < "$ST_REPOS"
  fi
  list_trees | while IFS=$'\t' read -r repo branch path s; do
    if tmux_has_session "$s" 2>/dev/null &&
       claim_tree_session "$s" "$repo" "$branch" "$path"; then live='open'; else live='closed'; fi
    if [ "$live" = open ]; then agent=$(agent_status "$s"); else agent='-'; fi
    if [ -n "$(git -C "$path" status --porcelain 2>/dev/null)" ]; then changes='modified'; else changes='clean'; fi
    case $mains in
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
  local s all query
  if [ $# -gt 0 ]; then
    query=$1
    all=$(known_sessions)
    s=$(session_for_label "$query" || true)
    if [ -z "$s" ]; then
      case $'\n'"$all"$'\n' in
        *$'\n'"$query"$'\n'*) s=$query;;
        *) s=$(printf '%s\n' "$all" | grep -i -- "$query" | head -1) || true;;
      esac
    fi
    [ -n "$s" ] || die "no tree matching: $query"
  elif [ -n "${TMUX:-}" ]; then
    s=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S')
    all=$(known_sessions)
    case $'\n'"$all"$'\n' in
      *$'\n'"$s"$'\n'*) ;;
      *) die "$s is not a supertree session";;
    esac
  else
    die "usage: st status <tree>  (omit tree inside tmux)"
  fi
  if tmux_has_session "$s" 2>/dev/null && ! owned_tree_session "$s"; then
    die "tmux session '$s' is not owned by supertree"
  fi
  printf '%s\t%s\n' "$(label_for_session "$s")" "$(agent_status "$s")"
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

  local main repo slug dir sess dirty unpushed current old_sess current_sess=''
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
     tmux_has_session "$old_sess" 2>/dev/null &&
     claim_tree_session "$old_sess" "$repo" "$branch" "$dir"; then
    die "an older session for $branch is still open; close it before removing the tree"
  fi

  dirty=$(git -C "$dir" status --porcelain 2>/dev/null || true)
  if git -C "$dir" rev-parse --abbrev-ref '@{u}' >/dev/null 2>&1; then
    unpushed=$(git -C "$dir" log --format=%s '@{u}..' 2>/dev/null || true)
  elif [ -n "$(git -C "$dir" remote)" ]; then
    unpushed=$(git -C "$dir" log --format=%s HEAD --not --remotes 2>/dev/null || true)
  else
    unpushed=''   # no remote configured, nothing can be "unpushed"
  fi

  if [ "$force" = 0 ] && [ -n "$dirty$unpushed" ]; then
    [ -n "$dirty" ]    && { printf 'uncommitted changes:\n%s\n' "$dirty" >&2; }
    [ -n "$unpushed" ] && { printf 'unpushed commits:\n%s\n' "$unpushed" >&2; }
    die "refusing to remove $branch — commit/push, or pass --force"
  fi

  printf 'will remove:\n  tree      %s/%s\n  session   close\n  checkout  remove\n  branch    delete if merged\n' "$(basename "$main")" "$branch" >&2
  printf 'proceed? [y/N] ' >&2; read -r ans
  case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac

  current=$(branch_worktree "$main" "$branch" || true)
  [ "$current" = "$dir" ] || die "worktree for $branch changed; refusing to remove $dir"

  if [ -n "${TMUX:-}" ]; then
    current_sess=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null || true)
  fi
  if tmux_has_session "$sess" 2>/dev/null && owned_tree_session "$sess"; then
    evacuate_clients "$sess"
    # Killing our own session kills this process, so close it only after cleanup.
    if [ "$sess" != "$current_sess" ]; then kill_owned_tree_session "$sess" 2>/dev/null || true; fi
  fi
  if [ "$force" = 1 ]; then git -C "$main" worktree remove --force "$dir"
  else git -C "$main" worktree remove "$dir"; fi
  git -C "$main" branch -d "$branch" >/dev/null 2>&1 || info "branch $branch kept (not merged)"
  clear_tree_state "$repo" "$slug" "$sess"
  info "removed $(basename "$main")/$branch"
  if [ "$sess" = "$current_sess" ]; then
    kill_tree_session "$sess" "$repo" "$branch" "$main" 2>/dev/null || true
  fi
}

clear_tree_state() {
  local repo=$1 slug=$2 sess=$3 tmp
  rm -f "$ST_STATE/idx/$repo/$slug" \
    "$ST_STATE/seen/$repo/$slug" \
    "$ST_STATE/seen/$repo/$slug.$ST_HARNESS" \
    "$ST_STATE/seen/$repo/$slug.claude" \
    "$ST_STATE/seen/$repo/$slug.codex" \
    "$ST_STATE/seen/$repo/$slug.openrouter"
  if [ -f "$ST_STATE/recent" ]; then
    tmp=$(mktemp "$ST_STATE/.recent.XXXXXX")
    grep -vxF -- "$sess" "$ST_STATE/recent" > "$tmp" || true
    mv "$tmp" "$ST_STATE/recent"
  fi
}

cmd_remove_all() {
  local force=0 root dir main branch sess repo slug dirty unpushed current other found tmp j
  local current_sess='' close_current=0 close_identity='' session_target=''
  local -a dirs=() mains=() branches=() sessions=() kinds=()
  case ${1:-} in --force|-f) force=1; shift;; esac
  [ $# -eq 0 ] || die "usage: st remove all [--force]"
  [ -d "$ST_WORKTREE_ROOT" ] || { info "no worktrees under $ST_WORKTREE_ROOT"; return 0; }
  root=$(cd "$ST_WORKTREE_ROOT" && pwd -P)
  [ "$root" != / ] && [ "$root" != "$HOME" ] || die "unsafe worktree root: $root"

  for dir in "$root"/* "$root"/*/*; do
    [ -d "$dir" ] && [ ! -L "$dir" ] && [ ! -L "$dir/.git" ] &&
      { [ -f "$dir/.git" ] || [ -d "$dir/.git" ]; } || continue
    [ "$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null || true)" = "$dir" ] || continue
    main=$(main_worktree "$dir")
    [ -n "$main" ] || continue
    branch=$(git -C "$dir" symbolic-ref --quiet --short HEAD 2>/dev/null || true)
    if [ "$main" = "$dir" ]; then
      # Do not list a nested checkout twice when its parent is a repository.
      case $dir in "$root"/*/*)
        [ -e "$(dirname "$dir")/.git" ] && continue;;
      esac
      sess=''
      if [ -n "$branch" ] && grep -qxF "$dir" "$ST_REPOS" 2>/dev/null; then
        sess=$(tree_session "$(repo_key "$dir")" "$branch")
      fi
      dirs+=("$dir"); mains+=("$main"); branches+=("$branch")
      sessions+=("$sess"); kinds+=(repo)
    else
      if [ -n "$branch" ]; then
        [ "$(branch_worktree "$main" "$branch" || true)" = "$dir" ] ||
          die "worktree changed while listing: $dir"
      fi
      repo=$(repo_key "$main")
      sess=$(tree_session "$repo" "${branch:-(detached)}")
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
    dirty=$(git -C "$dir" status --porcelain 2>/dev/null || true)
    unpushed=''
    if git -C "$dir" rev-parse --abbrev-ref '@{u}' >/dev/null 2>&1; then
      unpushed=$(git -C "$dir" log --format=%s '@{u}..' 2>/dev/null || true)
    elif [ -n "$(git -C "$dir" remote)" ]; then
      unpushed=$(git -C "$dir" log --format=%s HEAD --not --remotes 2>/dev/null || true)
    fi
    if [ "$force" = 0 ] && [ -n "$dirty$unpushed" ]; then
      die "refusing to remove $(basename "${mains[i]}")/${branches[i]:-(detached)} with uncommitted or unpushed work; pass --force"
    fi
  done
  printf 'proceed? [y/N] ' >&2; read -r ans
  case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac
  if [ -n "${TMUX:-}" ]; then
    current_sess=$(tmux display-message -p '#S' 2>/dev/null || true)
  fi

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
        if tmux_has_session "$sess" 2>/dev/null && owned_tree_session "$sess"; then
          evacuate_clients "$sess"
          if [ "$sess" = "$current_sess" ]; then
            close_current=1
            session_target=$(tmux_session_id "$sess") || session_target=''
            if [ -n "$session_target" ]; then
              close_identity=$(tmux show-options -qv -t "$session_target" @supertree_identity 2>/dev/null || true)
            fi
          else kill_owned_tree_session "$sess" 2>/dev/null || true; fi
        fi
        if [ "$force" = 1 ]; then git -C "$main" worktree remove --force "$dir"
        else git -C "$main" worktree remove "$dir"; fi
        [ -z "$branch" ] || git -C "$main" branch -d "$branch" >/dev/null 2>&1 ||
          info "branch $branch kept (not merged)"
        clear_tree_state "$repo" "$(branch_key "${branch:-(detached)}")" "$sess"
      else
        if [ -n "$sess" ] && tmux_has_session "$sess" 2>/dev/null &&
           owned_tree_session "$sess"; then
          evacuate_clients "$sess"
          if [ "$sess" = "$current_sess" ]; then
            close_current=1
            session_target=$(tmux_session_id "$sess") || session_target=''
            if [ -n "$session_target" ]; then
              close_identity=$(tmux show-options -qv -t "$session_target" @supertree_identity 2>/dev/null || true)
            fi
          else kill_owned_tree_session "$sess" 2>/dev/null || true; fi
        fi
        rm -rf -- "$dir"
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
    kill_identified_session "$current_sess" "$close_identity" 2>/dev/null || true
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
