# shellcheck shell=bash
# supertree bootstrap module

# Prints the start point for a new branch: --from wins over ST_BASE_BRANCH.
# Hook output goes to stderr so it cannot leak into that result.
pre_create() {
  local main=$1 branch=$2 from=$3
  [ -n "$trusted_config_snapshot" ] || { printf '%s' "$from"; return 0; }
  (
    ST_BASE_BRANCH='' ST_PRE_CREATE=''
    # shellcheck disable=SC1090
    . "$trusted_config_snapshot"
    base=${from:-$ST_BASE_BRANCH}
    if [ -n "$ST_PRE_CREATE" ]; then
      info "pre-create hook"
      cd "$main" || exit 1
      export ST_TREE_BRANCH="$branch" ST_BASE_BRANCH="$base"
      eval "$ST_PRE_CREATE" >&2 || exit 1
    fi
    printf '%s' "$base"
  )
}

bootstrap() {
  local main=$1 dir=$2 repo=$3 branch=$4 idx=$5
  local -a ST_LINK_DIRS=(node_modules)
  local -a ST_COPY_GLOBS=('.env*')
  local -a ST_LOCKFILES=(package-lock.json yarn.lock pnpm-lock.yaml bun.lockb)
  local ST_INSTALL_CMD='npm ci'
  local ST_POST_CREATE=''

  # shellcheck disable=SC1091
  [ -n "$trusted_config_snapshot" ] && . "$trusted_config_snapshot"

  # macOS bash 3.2 treats an empty array as unbound under set -u, hence ${a[@]+...}.
  local locks_match=1 needs_install=0 l d g f
  for l in ${ST_LOCKFILES[@]+"${ST_LOCKFILES[@]}"}; do
    [ -f "$main/$l" ] || continue
    cmp -s "$main/$l" "$dir/$l" || locks_match=0
  done

  for d in ${ST_LINK_DIRS[@]+"${ST_LINK_DIRS[@]}"}; do
    [ -d "$main/$d" ] || continue
    [ -e "$dir/$d" ] && continue
    if [ "$locks_match" = 1 ]; then
      ln -s "$main/$d" "$dir/$d"
      info "linked $d -> main checkout"
    else
      needs_install=1
    fi
  done

  if [ "$needs_install" = 1 ] && [ -n "$ST_INSTALL_CMD" ]; then
    info "lockfile differs from main, running: $ST_INSTALL_CMD"
    ( cd "$dir" && eval "$ST_INSTALL_CMD" )
  fi

  for g in ${ST_COPY_GLOBS[@]+"${ST_COPY_GLOBS[@]}"}; do
    for f in "$main"/$g; do
      [ -e "$f" ] || continue
      cp -R "$f" "$dir/"
      info "copied $(basename "$f")"
    done
  done

  if [ -n "$ST_POST_CREATE" ]; then
    info "post-create hook"
    ( cd "$dir"
      export ST_TREE_DIR="$dir" ST_TREE_BRANCH="$branch" ST_TREE_INDEX="$idx"
      eval "$ST_POST_CREATE" )
  fi
}
