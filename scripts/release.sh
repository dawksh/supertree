#!/usr/bin/env bash
set -euo pipefail

# Every first-parent commit after the latest published release gets a release.
# The first run begins at the commit that introduced this workflow, so older
# unreleased commits are included in that first release instead of backfilled.
# A commit that fails its checks is folded into the next release, unless it is
# the newest: otherwise one broken merge would block every release after it.

next_version() {
  local tag=$1 bump=$2 major minor patch
  [[ $tag =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || {
    printf 'invalid release tag: %s\n' "$tag" >&2
    return 1
  }
  major=${BASH_REMATCH[1]}
  minor=${BASH_REMATCH[2]}
  patch=${BASH_REMATCH[3]}
  case $bump in
    major) printf 'v%d.0.0\n' "$((major + 1))";;
    minor) printf 'v%d.%d.0\n' "$major" "$((minor + 1))";;
    patch) printf 'v%d.%d.%d\n' "$major" "$minor" "$((patch + 1))";;
    *) printf 'invalid release bump: %s\n' "$bump" >&2; return 1;;
  esac
}

choose_bump() {
  local labels=$1
  if grep -qxF 'release:major' <<<"$labels"; then
    printf 'major\n'
  elif grep -qxF 'release:minor' <<<"$labels"; then
    printf 'minor\n'
  else
    printf 'patch\n'
  fi
}

check_source() {
  local src=$1 file
  for file in "$src/bin/st" "$src"/bin/st-lib/*.sh "$src/install.sh" "$src"/tests/*.sh "$src"/scripts/*.sh; do
    bash -n "$file" || return 1
  done
  for file in "$src"/tests/*.sh; do
    bash "$file" || return 1
  done
}

main() {
  local repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}
  local base sha labels bump next tmp src assets state tagged_sha pending newest carried=''
  local active=0 released=0

  cd "$(dirname "${BASH_SOURCE[0]}")/.."

  command -v gh >/dev/null || { printf 'gh is required\n' >&2; return 1; }
  command -v sha256sum >/dev/null || { printf 'sha256sum is required\n' >&2; return 1; }
  git fetch --quiet origin main --tags
  base=$(gh api "repos/$repo/releases/latest" --jq '.tag_name')
  [[ $base =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    printf 'latest published release has an invalid tag: %s\n' "$base" >&2
    return 1
  }
  git merge-base --is-ancestor "$base" origin/main || {
    printf 'latest release %s is not on main\n' "$base" >&2
    return 1
  }

  pending=$(git rev-list --first-parent --reverse "$base..origin/main")
  newest=${pending##*$'\n'}
  # On activation, skip old commits whose source did not contain this pipeline.
  while IFS= read -r sha; do
    [[ -n $sha ]] || continue
    if [[ $active == 0 ]]; then
      git cat-file -e "$sha:.github/workflows/release.yml" 2>/dev/null || continue
      active=1
    fi
    labels=$(gh api "repos/$repo/commits/$sha/pulls" \
      --jq '.[] | select(.merged_at != null) | .labels[].name')
    labels=$carried$labels
    bump=$(choose_bump "$labels")
    next=$(next_version "$base" "$bump")
    printf 'Releasing %s as %s (%s)\n' "$sha" "$next" "$bump"

    tmp=$(mktemp -d)
    src="$tmp/src"
    assets="$tmp/assets"
    mkdir -p "$src" "$assets"
    git archive "$sha" | tar -x -C "$src"
    if ! check_source "$src"; then
      [[ $sha != "$newest" ]] || return 1
      printf 'Skipping %s: checks failed; its changes ship with the next release.\n' "$sha" >&2
      carried=$labels$'\n'
      rm -rf "$tmp"
      continue
    fi
    carried=''

    tar -czf "$assets/st.tar.gz" -C "$src/bin" st st-lib
    cp "$src/scripts/legacy-upgrade.sh" "$assets/st"
    cp "$src/install.sh" "$assets/install.sh"
    cp "$src/tmux/supertree.conf" "$assets/supertree.conf"
    grep -qx "ST_VERSION='dev'" "$src/bin/st"
    grep -qx "ST_VERSION='dev'" "$assets/st"
    (cd "$assets" && sha256sum st st.tar.gz install.sh supertree.conf > SHA256SUMS)

    # A failed upload leaves a draft; the next run resumes it with fresh assets.
    state=$(gh release view "$next" --json isDraft --jq '.isDraft' 2>/dev/null || true)
    tagged_sha=$(git rev-parse -q --verify "refs/tags/$next^{commit}" 2>/dev/null || true)
    if [[ -n $tagged_sha && $tagged_sha != "$sha" ]]; then
      printf '%s points to %s, expected %s\n' "$next" "$tagged_sha" "$sha" >&2
      return 1
    fi
    if [[ -n $state && $tagged_sha != "$sha" ]]; then
      printf 'release %s exists without its expected tag\n' "$next" >&2
      return 1
    fi
    if [[ $state == false ]]; then
      printf 'Release %s already published; continuing.\n' "$next"
    else
      if [[ $state != true ]]; then
        gh release create "$next" --target "$sha" --draft \
          --title "$next" --generate-notes --notes-start-tag "$base"
      fi
      gh release upload "$next" "$assets/st" "$assets/st.tar.gz" "$assets/install.sh" \
        "$assets/supertree.conf" "$assets/SHA256SUMS" --clobber
      gh release edit "$next" --draft=false --latest
    fi
    base=$next
    released=$((released + 1))
    rm -rf "$tmp"
  done <<<"$pending"
  if ((released == 0)); then
    printf 'No commits pending release.\n'
  fi
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
