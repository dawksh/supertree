# shellcheck shell=bash
# supertree repository module

trusted_config_snapshot=''

main_worktree() {
  git -C "${1:-.}" worktree list --porcelain 2>/dev/null |
    awk 'NR==1 && /^worktree /{print substr($0,10)}'
}

require_repo() {
  local m
  m=$(main_worktree "${1:-.}") || true
  [ -n "$m" ] || die "not inside a git repository"
  printf '%s' "$m"
}

register_repo() {
  mkdir -p "$ST_STATE"
  grep -qxF "$1" "$ST_REPOS" 2>/dev/null || printf '%s\n' "$1" >> "$ST_REPOS"
}

# Resolved like git's own worktree paths, so list_trees rows compare equal
# even when a repos entry runs through a symlink (macOS /var -> /private/var).
main_checkouts() {
  local r
  [ -f "$ST_REPOS" ] || return 0
  while IFS= read -r r; do
    (cd "$r" >/dev/null 2>&1 && pwd -P) || true
  done < "$ST_REPOS"
}

trees_or_list() {
  if [ $# -gt 0 ]; then printf '%s\n' "$1"; else list_trees; fi
}

session_for_label() {
  local trees
  trees=$(trees_or_list "${@:2}")
  ST_LABEL=$1 awk -F '\t' '
    { r = $1; sub(/-[^-]*$/, "", r) }
    r "/" $2 == ENVIRON["ST_LABEL"] { print $4; found = 1; exit }
    END { exit !found }' <<< "$trees"
}

label_for_session() {
  local trees
  trees=$(trees_or_list "${@:2}")
  ST_SESSION=$1 awk -F '\t' '
    $4 == ENVIRON["ST_SESSION"] { r = $1; sub(/-[^-]*$/, "", r); print r "/" $2; found = 1; exit }
    END { if (!found) printf "unknown tree" }' <<< "$trees"
}

# Exact label, then exact session name, then the picker's rule: the first
# label containing the query, ignoring case.
resolve_session() {
  local trees
  trees=$(trees_or_list "${@:2}")
  ST_QUERY=$1 awk -F '\t' '
    { label = $1; sub(/-[^-]*$/, "", label); label = label "/" $2; q = ENVIRON["ST_QUERY"] }
    label == q { print $4; exact = 1; exit }
    $4 == q && by_name == "" { by_name = $4 }
    partial == "" && index(tolower(label), tolower(q)) { partial = $4 }
    END {
      if (exact) exit 0
      if (by_name == "") by_name = partial
      if (by_name == "") exit 1
      print by_name
    }' <<< "$trees"
}

# A worktree whose folder was deleted by hand stays listed as "prunable"
# after its branch line, so a record only counts once it has ended.
branch_worktree() {
  local main=$1 branch=$2 line path='' match=0 stale=0
  while IFS= read -r line; do
    case $line in
      'worktree '*) path=${line#worktree }; match=0; stale=0;;
      'branch '*) [ "${line#branch }" != "refs/heads/$branch" ] || match=1;;
      detached) [ "$(tree_branch "$path")" != "$branch" ] || match=1;;
      prunable*) stale=1;;
      '')
        if [ "$match" = 1 ] && [ "$stale" = 0 ] && [ -d "$path" ]; then
          printf '%s' "$path"
          return 0
        fi
        match=0;;
    esac
  done < <(git -C "$main" worktree list --porcelain; echo)
  return 1
}

stale_branch_worktrees() {
  git -C "$1" worktree list --porcelain | ST_BRANCH="refs/heads/$2" awk '
    /^worktree /{ p = substr($0, 10); b = "" }
    /^branch /{ b = substr($0, 8) }
    /^prunable/ && b == ENVIRON["ST_BRANCH"] { print p }'
}

# Rebase, bisect and `checkout <sha>` detach HEAD. Keep naming the tree after
# its branch so it keeps its session, agent history and picker entry.
tree_branch() {
  local dir=$1 name git_dir base ref
  name=$(git -C "$dir" branch --show-current 2>/dev/null || true)
  if [ -n "$name" ]; then printf '%s' "$name"; return 0; fi
  git_dir=$(git -C "$dir" rev-parse --absolute-git-dir 2>/dev/null) || return 0
  name=$(cat "$git_dir/rebase-merge/head-name" "$git_dir/rebase-apply/head-name" \
    "$git_dir/BISECT_START" 2>/dev/null | head -1)
  name=${name#refs/heads/}
  if [ -n "$name" ] && git -C "$dir" show-ref --verify --quiet "refs/heads/$name"; then
    printf '%s' "$name"
    return 0
  fi
  # Otherwise match the directory name we created the tree under. Never for
  # the main checkout, whose directory is named after the repo.
  [ "$git_dir" != "$(git -C "$dir" rev-parse --path-format=absolute --git-common-dir)" ] || return 0
  base=$(basename "$dir")
  while IFS= read -r ref; do
    [ "$(branch_key "$ref")" = "$base" ] || [ "$(short_key "$(branch_key "$ref")")" = "$base" ] ||
      [ "$(slugify "$ref")" = "$base" ] || continue
    git -C "$dir" worktree list --porcelain | grep -qxF "branch refs/heads/$ref" && continue
    printf '%s' "$ref"
    return 0
  done < <(git -C "$dir" for-each-ref --format='%(refname)' refs/heads |
    LC_ALL=C awk -v base="$base" '{
      sub(/^refs\/heads\//, "")
      label = $0; gsub(/[^A-Za-z0-9_-]/, "-", label)
      legacy = $0; gsub(/[\/.: ]/, "-", legacy)
      if (index(base, substr(label, 1, 48) "-") == 1 || legacy == base) print
    }')
}

managed_tree() {
  local main=$1 branch=$2 path=$3 repo slug
  repo=$(repo_key "$main")
  slug=$(branch_key "$branch")
  [ "$path" -ef "$(short_tree_dir "$repo" "$slug")" ] && return 0
  [ "$path" -ef "$(tree_dir "$repo" "$slug")" ] && return 0
  [ "$path" -ef "$(tree_dir "$(basename "$main")" "$(slugify "$branch")")" ]
}

prepare_repo_config() {
  local main=$1 expected actual
  [ -f "$main/.supertree" ] || return 0
  trusted_config_snapshot=$(mktemp)
  trap 'rm -f "${trusted_config_snapshot:-}"' EXIT
  cp "$main/.supertree" "$trusted_config_snapshot"
  actual=$(config_fingerprint "$trusted_config_snapshot")
  expected=$(cat "$ST_STATE/trust/$(repo_key "$main")" 2>/dev/null || true)
  [ "$actual" = "$expected" ] ||
    die "untrusted or changed $main/.supertree; review it, then run 'st trust' in that repository"
}

tree_index() {
  local repo=$1 slug=$2 old_repo=$3 f="$ST_STATE/idx/$1/$2" lock="$ST_STATE/idx/.lock" n tries=0
  if [ -f "$f" ]; then cat "$f"; return; fi
  mkdir -p "$ST_STATE/idx/$repo"
  # mkdir is atomic, so two concurrent 'st new' runs cannot take the same index.
  until mkdir "$lock" 2>/dev/null; do
    tries=$((tries + 1))
    [ "$tries" -lt 50 ] || die "index lock is held; remove $lock if no other st is running"
    sleep 0.1
  done
  if [ ! -f "$f" ]; then
    # Only whole numbers count: anything else would abort the arithmetic with the lock held.
    n=$(cat "$ST_STATE/idx/$repo"/* "$ST_STATE/idx/$old_repo"/* 2>/dev/null |
      awk '/^[0-9]+$/ && $1 + 0 > max { max = $1 + 0 } END { print max + 0 }' || true)
    printf '%s\n' "$(( ${n:-0} + 1 ))" > "$f" || { rmdir "$lock"; die "cannot write $f"; }
  fi
  rmdir "$lock"
  cat "$f"
}

# Sessions opened before readable names keep running under the new name.
adopt_hashed_session() {
  local repo=$1 branch=$2 sess=$3 hashed
  hashed=$(sess_name "$repo" "$(branch_key "$branch")")
  [ "$hashed" != "$sess" ] || return 0
  tmux has-session -t "=$hashed" 2>/dev/null || return 0
  ! tmux has-session -t "=$sess" 2>/dev/null || return 0
  tmux rename-session -t "=$hashed" "$sess" 2>/dev/null || true
}

# A session still under its pre-readable hashed name is this tree's session.
# Listing is read-only, so it reports that name instead of renaming it.
live_hashed_session() {
  local repo=$1 branch=$2 sess=$3 live=$4 hashed
  hashed=$(sess_name "$repo" "$(branch_key "$branch")")
  case $'\n'"$live"$'\n' in
    *$'\n'"$sess"$'\n'*) printf '%s' "$sess";;
    *$'\n'"$hashed"$'\n'*) printf '%s' "$hashed";;
    *) printf '%s' "$sess";;
  esac
}

list_repo_trees() {
  local r=$1 root=$2 repo branch path orphan
  [ -d "$r" ] || return 0
  # A moved or broken checkout in the repos list must not fail every listing.
  git -C "$r" rev-parse --git-dir >/dev/null 2>&1 || return 0
  repo=$(repo_key "$r")
    # Skip linked worktrees made by other tools (e.g. Cursor) outside our root.
    # Flag the main checkout as orphaned when no worktree under our root is left.
    # Rows wait for the record end: "prunable" (dir deleted) comes after "branch".
    # https://git-scm.com/docs/git-worktree#_porcelain_format
  git -C "$r" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" -v root="$root/" '
      function flush() { if (row != "") { if (n == 1) main = row; else linked[++k] = row }; row = "" }
      /^worktree /{flush(); p=substr($0,10); n++; keep = n == 1 || index(p, root) == 1}
      keep && /^branch /{b=$2; sub("refs/heads/","",b); row = repo"\t"b"\t"p}
      keep && /^detached$/{row = repo"\t(detached)\t"p}
      /^prunable/{row = ""}
      END {
        flush()
        if (main != "") print main "\t" (k ? 0 : 1)
        for (i = 1; i <= k; i++) print linked[i] "\t0"
      }' | while IFS=$'\t' read -r repo branch path orphan; do
        [ "$branch" != '(detached)' ] || branch=$(tree_branch "$path")
        printf '%s\t%s\t%s\t%s\n' "$repo" "${branch:-(detached)}" "$path" "$orphan"
      done
}

# Rows: repo key, branch, path, tmux session. Session names stay readable
# (repo/branch); a short hash is added only where two trees would collide.
list_trees() {
  local r repo branch path sess clash root live batch count=0 i
  [ -f "$ST_REPOS" ] || return 0
  live=$(tmux list-sessions -F '#S' 2>/dev/null || true)
  root=$(cd "$ST_WORKTREE_ROOT" 2>/dev/null && pwd -P || printf '%s' "$ST_WORKTREE_ROOT")
  batch=$(mktemp -d)
  {
    while IFS= read -r r; do
      [ -d "$r" ] || continue
      count=$((count + 1))
      list_repo_trees "$r" "$root" > "$batch/$count" &
      if [ "$count" -eq 8 ]; then
        wait
        for ((i=1; i<=count; i++)); do cat "$batch/$i"; rm "$batch/$i"; done
        count=0
      fi
    done < "$ST_REPOS"
    wait
    for ((i=1; i<=count; i++)); do cat "$batch/$i"; done
    rm -rf "$batch"
  } | awk -F '\t' '
    {
      row[NR] = $0; branch[NR] = $2; name = $1; sub(/-[^-]*$/, "", name)
      hash = $1; sub(/.*-/, "", hash)
      repo_name[NR] = name; repo_hash[NR] = substr(hash, 1, 6)
      if (!((name, $1) in seen_repo)) { seen_repo[name, $1] = 1; repos[name]++ }
    }
    END {
      for (i = 1; i <= NR; i++) {
        # tmux reserves . and : in targets and expands # as a format (#h is the hostname).
        b = branch[i]; gsub(/[.:#]/, "_", b)
        sess[i] = repo_name[i] (repos[repo_name[i]] > 1 ? "-" repo_hash[i] : "") "/" b
        if (!((sess[i], branch[i]) in seen_tree)) { seen_tree[sess[i], branch[i]] = 1; trees[sess[i]]++ }
      }
      for (i = 1; i <= NR; i++) print row[i] "\t" sess[i] "\t" (trees[sess[i]] > 1)
    }' | while IFS=$'\t' read -r repo branch path orphan sess clash; do
    [ "$clash" = 0 ] || sess="$sess-$(identity_hash "$branch" | cut -c1-6)"
    case $'\n'"$live" in *$'\n'"$repo/"*) sess=$(live_hashed_session "$repo" "$branch" "$sess" "$live");; esac
    [ "$orphan" = 0 ] || tmux has-session -t "=$sess" 2>/dev/null || continue
    printf '%s\t%s\t%s\t%s\n' "$repo" "$branch" "$path" "$sess"
  done
}

tree_session() {
  local trees
  trees=$(trees_or_list "${@:3}")
  awk -F '\t' -v repo="$1" -v branch="$2" \
    '$1 == repo && $2 == branch && !found { print $4; found = 1 }' <<< "$trees"
}
