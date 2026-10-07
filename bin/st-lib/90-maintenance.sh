# shellcheck shell=bash
# supertree maintenance module

cmd_doctor() {
  local ok=0 b deps='tmux git fzf' agent_bin windows
  windows=$(configured_windows)
  window_enabled vim && deps="$deps nvim"
  if window_enabled agent; then agent_bin=$(harness_bin); deps="$deps $agent_bin"; fi
  for b in $deps; do
    if command -v "$b" >/dev/null 2>&1; then printf '  ok    %s\n' "$b"
    else printf '  MISS  %s\n' "$b"; ok=1; fi
  done
  local self; self=$(command -v st 2>/dev/null) || self=""
  if [ -n "$self" ]; then printf '  ok    st on PATH (%s)\n' "$self"
  else printf '  MISS  st not on PATH\n'; ok=1; fi
  if grep -q 'supertree.conf' "$HOME/.tmux.conf" 2>/dev/null; then printf '  ok    ~/.tmux.conf sources supertree.conf\n'
  else printf '  MISS  ~/.tmux.conf does not source supertree.conf\n'; ok=1; fi
  printf '  --    worktree root %s\n' "$ST_WORKTREE_ROOT"
  printf '  --    repos known   %s\n' "$( [ -f "$ST_REPOS" ] && wc -l < "$ST_REPOS" | tr -d ' ' || echo 0)"
  printf '  --    harness       %s (%s)\n' "$(harness_label)" "$ST_HARNESS"
  printf '  --    windows       %s\n' "$(printf '%s' "$windows" | tr '\n' ' ')"
  printf '  --    config        %s\n' "$ST_CONFIG"
  return $ok
}

cmd_update() {
  [ $# -eq 0 ] || die "usage: st update"
  command -v curl >/dev/null 2>&1 || die "curl is required to update st"
  command -v tar >/dev/null 2>&1 || die "tar is required to update st"

  local release_root latest_url latest self prefix tmp candidate staged staged_lib cleanup previous_lib
  release_root=${ST_RELEASE_ROOT:-https://github.com/dawksh/supertree}
  latest_url=$(curl -fsS -o /dev/null -w '%{redirect_url}' "$release_root/releases/latest") ||
    die "could not check for updates"
  latest=${latest_url##*/}
  case $latest in
    ''|*[!A-Za-z0-9._-]*) die "could not determine the latest release";;
  esac

  if [ "$ST_VERSION" = "$latest" ]; then
    info "st $ST_VERSION is already the latest version"
    return 0
  fi
  [ "$ST_VERSION" != dev ] || die "development install detected — update it with git"

  self=$(st_executable)
  [ ! -L "$self" ] || die "source install detected — update it with git"
  prefix=$(dirname "$self")
  [ -w "$prefix" ] || die "cannot update $self (directory is not writable)"

  tmp=$(mktemp -d)
  staged=$(mktemp "$prefix/.st.update.XXXXXX")
  staged_lib=$(mktemp -d "$prefix/.st-lib.update.XXXXXX")
  printf -v cleanup 'rm -rf %q %q %q' "$tmp" "$staged" "$staged_lib"
  trap "$cleanup" EXIT

  curl -fsSL "$release_root/releases/download/$latest/st.tar.gz" -o "$tmp/st.tar.gz" ||
    die "could not download st $latest"
  tar -tzf "$tmp/st.tar.gz" | awk '
    $0 == "st" || $0 == "st-lib/" || $0 ~ /^st-lib\/[A-Za-z0-9_.-]+\.sh$/ { next }
    { exit 1 }
  ' || die "downloaded update archive has unexpected paths"
  mkdir "$tmp/package"
  tar -xzf "$tmp/st.tar.gz" -C "$tmp/package" ||
    die "could not unpack st $latest"
  candidate="$tmp/package/st"
  head -1 "$candidate" | grep -q '^#!/usr/bin/env bash$' ||
    die "downloaded update does not look like st"
  sed "s|^ST_VERSION='dev'$|ST_VERSION='$latest'|" "$candidate" > "$tmp/stamped"
  candidate="$tmp/stamped"
  grep -qx "ST_VERSION='$latest'" "$candidate" ||
    die "downloaded update has invalid version metadata"
  bash -n "$candidate" || die "downloaded update is not valid bash"
  [ -f "$tmp/package/st-lib/00-core.sh" ] ||
    die "downloaded update is missing its modules"
  for module in "$tmp/package"/st-lib/*.sh; do
    bash -n "$module" || die "downloaded module is not valid bash: $module"
  done

  install -m 0755 "$candidate" "$staged" || die "could not stage update beside $self"
  cp "$tmp/package"/st-lib/*.sh "$staged_lib/" ||
    die "could not stage update modules"
  previous_lib=''
  if [ -e "$prefix/st-lib-$latest" ]; then
    previous_lib=$(mktemp -d "$prefix/.st-lib.previous.XXXXXX")
    rmdir "$previous_lib"
    mv "$prefix/st-lib-$latest" "$previous_lib" ||
      die "could not stage existing update modules"
  fi
  if ! mv "$staged_lib" "$prefix/st-lib-$latest"; then
    [ -z "$previous_lib" ] || mv "$previous_lib" "$prefix/st-lib-$latest"
    die "could not install update modules"
  fi
  if ! mv -f "$staged" "$self"; then
    rm -rf "$prefix/st-lib-$latest"
    [ -z "$previous_lib" ] || mv "$previous_lib" "$prefix/st-lib-$latest"
    die "could not replace $self"
  fi
  [ -z "$previous_lib" ] || rm -rf "$previous_lib"
  info "updated st $ST_VERSION -> $latest"
}

cmd_version() { printf "st %s\n" "$ST_VERSION"; }

cmd_uninstall() {
  [ $# -eq 0 ] || die "usage: st uninstall"
  local self confdir tmux_conf fragment config staged sessions sess manifest key binding module_dir
  local had_fragment=0 current_sess='' close_current=0
  self=$(st_executable)
  [ -L "$self" ] || [ "$ST_VERSION" != dev ] ||
    die "run the installed st command, not the development source file"
  [ "$(basename "$self")" = st ] || die "unexpected executable path: $self"
  manifest="$self.install"
  confdir=${ST_CONFDIR:-}
  tmux_conf=${TMUX_CONF:-}
  if [ -f "$manifest" ]; then
    [ -n "$confdir" ] || confdir=$(sed -n '1p' "$manifest")
    [ -n "$tmux_conf" ] || tmux_conf=$(sed -n '2p' "$manifest")
  fi
  confdir=${confdir:-$HOME/.config/supertree}
  tmux_conf=${tmux_conf:-$HOME/.tmux.conf}
  fragment="$confdir/supertree.conf"
  [ ! -f "$fragment" ] || had_fragment=1
  config=${_env_config:-$confdir/config}
  if [ -d "$ST_STATE" ]; then
    local state_real
    state_real=$(cd "$ST_STATE" && pwd -P)
    [ "$state_real" != / ] && [ "$state_real" != "$HOME" ] ||
      die "unsafe state directory: $ST_STATE"
  fi
  printf 'will uninstall:\n  command  %s\n  bindings %s\n  config   %s\n  state    %s\n' \
    "$self" "$fragment" "$config" "$ST_STATE" >&2
  [ -f "$tmux_conf" ] && printf '  tmux     %s (Supertree source line)\n' "$tmux_conf" >&2
  printf 'proceed? [y/N] ' >&2; read -r ans
  case ${ans:-n} in y|Y|yes) ;; *) die "aborted";; esac

  sessions=$(known_sessions)
  if [ -n "${TMUX:-}" ]; then
    current_sess=$(tmux display-message -p '#S' 2>/dev/null || true)
  fi
  while IFS= read -r sess; do
    [ -n "$sess" ] || continue
    evacuate_clients "$sess"
    if [ "$sess" = "$current_sess" ]; then close_current=1; continue; fi
    tmux_kill_session "$sess" 2>/dev/null || true
  done <<EOF
$sessions
EOF

  if [ -f "$tmux_conf" ] && grep -qxF "source-file $fragment" "$tmux_conf"; then
    staged=$(mktemp "${tmux_conf}.st.XXXXXX")
    cp -p "$tmux_conf" "$staged"
    # Remove only the line installed for this fragment and its marker.
    awk -v target="source-file $fragment" '
      { if ($0 == target) { if (prev != "# supertree" && have) print prev; have=0; next }
        if (have) print prev; prev=$0; have=1 }
      END { if (have) print prev }
    ' "$tmux_conf" > "$staged"
    mv "$staged" "$tmux_conf"
  fi
  rm -f -- "$fragment" "$config"
  [ ! -d "$confdir" ] || rmdir "$confdir" 2>/dev/null || true
  if command -v tmux >/dev/null 2>&1 && tmux list-sessions >/dev/null 2>&1; then
    for key in M-w M-e M-Tab M-r M-1 M-2 M-3 M-q; do
      binding=$(tmux list-keys -T root "$key" 2>/dev/null || true)
      case $binding in *"$self"*) tmux unbind-key -n "$key" 2>/dev/null || true;; esac
    done
    if [ "$had_fragment" = 1 ]; then
      for key in M-q M-Q; do
        binding=$(tmux list-keys -T root "$key" 2>/dev/null || true)
        case $binding in *'detach-client'*) tmux unbind-key -n "$key" 2>/dev/null || true;; esac
      done
    fi
    if [ "$(tmux show-options -gv status-left 2>/dev/null || true)" = \
      ' #{?@supertree_label,#{@supertree_label},#S} ' ]; then
      tmux set-option -gu status-left 2>/dev/null || true
      [ "$(tmux show-options -gv status-left-length 2>/dev/null || true)" != 60 ] ||
        tmux set-option -gu status-left-length 2>/dev/null || true
    fi
    [ ! -f "$tmux_conf" ] || tmux source-file "$tmux_conf" 2>/dev/null || true
  fi
  rm -rf -- "$ST_STATE"
  if [ "$ST_VERSION" != dev ]; then
    for module_dir in "$(dirname "$self")"/st-lib-*; do
      [ -d "$module_dir" ] && [ ! -L "$module_dir" ] &&
        [ -f "$module_dir/00-core.sh" ] || continue
      rm -rf -- "$module_dir"
    done
  fi
  rm -f -- "$self" "$manifest"
  info "uninstalled st"
  if [ "$close_current" = 1 ]; then
    tmux_kill_session "$current_sess" 2>/dev/null || true
  fi
}


st_register_command 'update' cmd_update 'install the latest release'
st_register_command 'uninstall' cmd_uninstall 'remove the command, config, bindings, and state'
st_register_command 'doctor' cmd_doctor 'check the installation'
st_register_command 'version' cmd_version 'show the installed version'
