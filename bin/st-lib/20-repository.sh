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

session_for_label() {
  local wanted=$1 repo branch path sess
  while IFS=$'\t' read -r repo branch path sess; do
    if [ "$(tree_label "$repo" "$branch")" = "$wanted" ]; then
      printf '%s' "$sess"
      return 0
    fi
  done < <(list_trees)
  return 1
}

label_for_session() {
  local wanted=$1 repo branch path sess
  while IFS=$'\t' read -r repo branch path sess; do
    if [ "$sess" = "$wanted" ]; then
      tree_label "$repo" "$branch"
      return 0
    fi
  done < <(list_trees)
  printf 'unknown tree'
}

branch_worktree() {
  local main=$1 branch=$2 line path=''
  while IFS= read -r line; do
    case $line in
      'worktree '*) path=${line#worktree };;
      'branch '*)
        if [ "${line#branch }" = "refs/heads/$branch" ]; then
          printf '%s' "$path"
          return 0
        fi;;
    esac
  done < <(git -C "$main" worktree list --porcelain)
  return 1
}

managed_tree() {
  local main=$1 branch=$2 path=$3 repo slug
  repo=$(repo_key "$main")
  slug=$(branch_key "$branch")
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
  local repo=$1 slug=$2 old_repo=$3 f="$ST_STATE/idx/$1/$2" n
  if [ -f "$f" ]; then cat "$f"; return; fi
  mkdir -p "$ST_STATE/idx/$repo"
  n=$(cat "$ST_STATE/idx/$repo"/* "$ST_STATE/idx/$old_repo"/* 2>/dev/null | sort -n | tail -1)
  n=$(( ${n:-0} + 1 ))
  printf '%s\n' "$n" | tee "$f"
}

# Sessions opened before readable names keep running under the new name. A
# legacy session is eligible only when its Supertree label matches this exact
# tree; a same-name session without ownership metadata is left untouched.
adopt_hashed_session() {
  local repo=$1 branch=$2 path=$3 sess=$4 live=$'\n'$5$'\n' hashed target
  hashed=$(sess_name "$repo" "$(branch_key "$branch")")
  case $live in
    *$'\n'"$sess"$'\n'*)
      if claim_tree_session "$sess" "$repo" "$branch" "$path"; then
        printf '%s' "$sess"
      elif case $live in *$'\n'"$hashed"$'\n'*) true;; *) false;; esac &&
           claim_tree_session "$hashed" "$repo" "$branch" "$path"; then
        # The readable name is foreign. Keep using the legitimate legacy name.
        printf '%s' "$hashed"
      else
        printf '%s' "$sess"
      fi
      return;;
  esac
  case $live in *$'\n'"$hashed"$'\n'*) ;; *) printf '%s' "$sess"; return;; esac
  if target=$(claim_tree_session_target "$hashed" "$repo" "$branch" "$path") &&
     tmux rename-session -t "$target" "$sess" 2>/dev/null; then
    printf '%s' "$sess"
  else
    printf '%s' "$sess"
  fi
}

# Rows: repo key, branch, path, tmux session. Session names stay readable
# (repo/branch); a short hash is added only where two trees would collide.
list_trees() {
  local r repo branch path sess clash live root
  [ -f "$ST_REPOS" ] || return 0
  live=$(tmux list-sessions -F '#{session_name}' 2>/dev/null || true)
  root=$(cd "$ST_WORKTREE_ROOT" 2>/dev/null && pwd -P || printf '%s' "$ST_WORKTREE_ROOT")
  while read -r r; do
    [ -d "$r" ] || continue
    repo=$(repo_key "$r")
    # Skip linked worktrees made by other tools (e.g. Cursor) outside our root.
    # Flag the main checkout as orphaned when no worktree under our root is left.
    git -C "$r" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" -v root="$root/" '
      /^worktree /{p=substr($0,10); n++; keep = n == 1 || index(p, root) == 1; row = ""}
      keep && /^branch /{b=$2; sub("refs/heads/","",b); row = repo"\t"b"\t"p}
      keep && /^detached$/{row = repo"\t(detached)\t"p}
      row != "" { if (n == 1) main = row; else linked[++k] = row; row = "" }
      END {
        if (main != "") print main "\t" (k ? 0 : 1)
        for (i = 1; i <= k; i++) print linked[i] "\t0"
      }'
  done < "$ST_REPOS" | awk -F '\t' '
    {
      row[NR] = $0; branch[NR] = $2; name = $1; sub(/-[^-]*$/, "", name)
      hash = $1; sub(/.*-/, "", hash)
      repo_name[NR] = name; repo_hash[NR] = substr(hash, 1, 6)
      if (!((name, $1) in seen_repo)) { seen_repo[name, $1] = 1; repos[name]++ }
    }
    END {
      for (i = 1; i <= NR; i++) {
        b = branch[i]; gsub(/[.:]/, "_", b)
        sess[i] = repo_name[i] (repos[repo_name[i]] > 1 ? "-" repo_hash[i] : "") "/" b
        if (!((sess[i], branch[i]) in seen_tree)) { seen_tree[sess[i], branch[i]] = 1; trees[sess[i]]++ }
      }
      for (i = 1; i <= NR; i++) print row[i] "\t" sess[i] "\t" (trees[sess[i]] > 1)
    }' | while IFS=$'\t' read -r repo branch path orphan sess clash; do
    [ "$clash" = 0 ] || sess="$sess-$(identity_hash "$branch" | cut -c1-6)"
    sess=$(adopt_hashed_session "$repo" "$branch" "$path" "$sess" "$live")
    [ "$orphan" = 0 ] || {
      tmux_has_session "$sess" 2>/dev/null &&
        claim_tree_session "$sess" "$repo" "$branch" "$path"
    } || continue
    printf '%s\t%s\t%s\t%s\n' "$repo" "$branch" "$path" "$sess"
  done
}

tree_session() {
  list_trees | awk -F '\t' -v repo="$1" -v branch="$2" \
    '$1 == repo && $2 == branch && !found { print $4; found = 1 }'
}
