# shellcheck shell=bash
# supertree maintenance module

installed_tmux_conf() {
  local tmux_conf=${TMUX_CONF:-} manifest
  manifest="$(st_executable).install"
  [ -n "$tmux_conf" ] || [ ! -f "$manifest" ] || tmux_conf=$(sed -n '2p' "$manifest")
  printf '%s' "${tmux_conf:-$HOME/.tmux.conf}"
}

installed_fragment() {
  local confdir=${ST_CONFDIR:-} manifest
  manifest="$(st_executable).install"
  [ -n "$confdir" ] || [ ! -f "$manifest" ] || confdir=$(sed -n '1p' "$manifest")
  printf '%s/supertree.conf' "${confdir:-$HOME/.config/supertree}"
}

# Compare the live root key table with tmux's own parsing of the generated
# fragment on an isolated server. This catches later bindings, unbinds, and
# sourced files that replace Supertree keys without guessing tmux syntax.
audit_bindings() {
  local fragment=$1 mode=${2:-doctor} socket key expected actual duplicates=0 mismatches=0
  local keys
  [ -f "$fragment" ] || { printf '  MISS  bindings file missing: %s\n' "$fragment"; return 1; }
  keys=$(awk '$1 == "bind" && $2 == "-n" { print $3 }' "$fragment" | sort -u)
  [ -n "$keys" ] || { printf '  MISS  no Supertree keys in %s\n' "$fragment"; return 1; }
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    if [ "$(awk -v key="$key" '$1 == "bind" && $2 == "-n" && $3 == key { n++ } END { print n+0 }' "$fragment")" -gt 1 ]; then
      printf '  WARN  %s is assigned more than once in %s; the last binding wins\n' "$key" "$fragment"
      duplicates=1
    fi
  done <<EOF
$keys
EOF
  [ "$duplicates" = 0 ] ||
    printf '  --    assign unique ST_KEY_* values in %s, then run st repair\n' "$ST_CONFIG"
  if ! tmux list-sessions >/dev/null 2>&1; then
    printf '  --    no live tmux server; key overrides cannot be checked\n'
    [ "$duplicates" = 0 ]; return $?
  fi
  socket="st-binding-audit-$$-$RANDOM"
  if ! tmux -L "$socket" -f /dev/null new-session -d -s audit 'sleep 30' >/dev/null 2>&1 ||
     ! tmux -L "$socket" source-file "$fragment" >/dev/null 2>&1; then
    tmux -L "$socket" kill-server >/dev/null 2>&1 || true
    printf '  MISS  tmux could not parse %s\n' "$fragment"
    return 1
  fi
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    expected=$(tmux -L "$socket" list-keys -T root "$key" 2>/dev/null || true)
    actual=$(tmux list-keys -T root "$key" 2>/dev/null || true)
    [ "$expected" != "$actual" ] || continue
    mismatches=1
    if [ -n "$actual" ]; then
      printf '  WARN  %s is overridden by: %s\n' "$key" "$actual"
    else
      printf '  WARN  %s is unbound in the live tmux server\n' "$key"
    fi
    printf '        Supertree expects: %s\n' "$expected"
  done <<EOF
$keys
EOF
  tmux -L "$socket" kill-server >/dev/null 2>&1 || true
  if [ "$mismatches" = 1 ]; then
    if [ "$mode" = repair ]; then
      printf '  --    reloading Supertree keys; move its source line after conflicting bindings in %s\n' "$(installed_tmux_conf)"
    else
      printf '  --    run st repair, then check bindings after the Supertree source line in %s\n' "$(installed_tmux_conf)"
    fi
  fi
  [ "$duplicates" = 0 ] && [ "$mismatches" = 0 ]
}

cmd_doctor() {
  local ok=0 b deps='tmux git fzf' agent_bin windows type tab tmux_conf
  local self fragment split_key
  windows=$(configured_windows)
  window_enabled vim && deps="$deps nvim"
  if window_enabled agent; then agent_bin=$(harness_bin); deps="$deps $agent_bin"; fi
  for type in $windows; do
    case $type in agent|vim|shell) continue;; esac
    tab=$(tab_command "$type")
    deps="$deps ${tab%% *}"
  done
  [ "${ST_SIDEBAR:-1}" = 0 ] || deps="$deps curl"
  for b in $deps; do
    if command -v "$b" >/dev/null 2>&1; then printf '  ok    %s\n' "$b"
    else printf '  MISS  %s\n' "$b"; ok=1; fi
  done
  self=$(command -v st 2>/dev/null) || self=""
  if [ -n "$self" ]; then printf '  ok    st on PATH (%s)\n' "$self"
  else printf '  MISS  st not on PATH\n'; ok=1; fi
  tmux_conf=$(installed_tmux_conf)
  fragment=$(installed_fragment)
  if [ -f "$ST_CONFIG" ] && ! bash -n "$ST_CONFIG" >/dev/null 2>&1; then
    printf '  MISS  %s has invalid shell syntax\n' "$ST_CONFIG"; ok=1
  fi
  if { [ "$ST_VERSION" = dev ] && grep -qF 'supertree.conf' "$tmux_conf" 2>/dev/null; } ||
     grep -qxF "source-file $fragment" "$tmux_conf" 2>/dev/null; then
    printf '  ok    %s sources supertree.conf\n' "$tmux_conf"
  else printf '  MISS  %s does not source supertree.conf\n' "$tmux_conf"; ok=1; fi
  if [ "$ST_VERSION" != dev ]; then
    split_key=${ST_KEY_SPLIT:-M-v}
    if [ -f "$fragment" ] && awk -v key="$split_key" '
      index($0, "bind -n " key " ") == 1 && / split"$/ { found=1 }
      END { exit !found }
    ' "$fragment"; then
      printf '  ok    %s binds %s to split\n' "$fragment" "$split_key"
    else
      printf '  MISS  %s does not bind %s to split\n' "$fragment" "$split_key"; ok=1
    fi
    audit_bindings "$fragment" doctor || ok=1
  fi
  printf '  --    worktree root %s\n' "$ST_WORKTREE_ROOT"
  printf '  --    repos known   %s\n' "$( [ -f "$ST_REPOS" ] && wc -l < "$ST_REPOS" | tr -d ' ' || echo 0)"
  printf '  --    harness       %s (%s)\n' "$(harness_label)" "$ST_HARNESS"
  printf '  --    windows       %s\n' "$(printf '%s\n' "$windows" | while read -r type; do window_name "$type"; printf ' '; done | sed 's/ $//')"
  printf '  --    config        %s\n' "$ST_CONFIG"
  return $ok
}

cmd_update() {
  [ $# -eq 0 ] || die "usage: st update"
  command -v curl >/dev/null 2>&1 || die "curl is required to update st"
  command -v tar >/dev/null 2>&1 || die "tar is required to update st"
  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
    die "sha256sum or shasum is required to update st"

  local release_root latest_url latest self prefix confdir manifest tmp cleanup expected_sum actual_sum
  release_root=${ST_RELEASE_ROOT:-https://github.com/dawksh/supertree}
  printf 'st: checking for updates...\n'
  latest_url=$(curl -fsS --connect-timeout 10 --max-time 30 -o /dev/null -w '%{redirect_url}' "$release_root/releases/latest") ||
    die "could not check for updates"
  latest=${latest_url##*/}
  case $latest in
    ''|*[!A-Za-z0-9._-]*) die "could not determine the latest release";;
  esac

  [ "$ST_VERSION" != dev ] || die "development install detected — update it with git"
  if [ "$ST_VERSION" = "$latest" ]; then
    printf 'st: %s is already the latest version\n' "$ST_VERSION"
    return 0
  fi

  self=$(st_executable)
  [ ! -L "$self" ] || die "source install detected — update it with git"
  prefix=$(dirname "$self")
  [ -w "$prefix" ] || die "cannot update $self (directory is not writable)"
  manifest="$self.install"
  confdir=${ST_CONFDIR:-}
  [ -n "$confdir" ] || [ ! -f "$manifest" ] || confdir=$(sed -n '1p' "$manifest")
  confdir=${confdir:-$HOME/.config/supertree}
  tmp=$(mktemp -d)
  printf -v cleanup 'rm -rf %q' "$tmp"
  trap "$cleanup" EXIT
  printf 'st: downloading %s...\n' "$latest"
  curl -fsSL --connect-timeout 10 --max-time 60 "$release_root/releases/download/$latest/install.sh" -o "$tmp/install.sh" ||
    die "could not download installer for st $latest"
  curl -fsSL --connect-timeout 10 --max-time 60 "$release_root/releases/download/$latest/SHA256SUMS" -o "$tmp/SHA256SUMS" ||
    die "could not download checksums for st $latest"
  expected_sum=$(awk '$2 == "install.sh" { print $1 }' "$tmp/SHA256SUMS")
  if command -v sha256sum >/dev/null 2>&1; then actual_sum=$(sha256sum "$tmp/install.sh")
  else actual_sum=$(shasum -a 256 "$tmp/install.sh"); fi
  [ -n "$expected_sum" ] && [ "$expected_sum" = "${actual_sum%% *}" ] ||
    die "installer for st $latest does not match its published checksum; nothing was changed"
  bash -n "$tmp/install.sh" || die "downloaded installer is not valid bash"
  (
    unset ST_HARNESS ST_WINDOWS ST_CONFIG
    ST_VERSION=$latest ST_PREFIX=$prefix ST_CONFDIR=$confdir \
      TMUX_CONF="$(installed_tmux_conf)" ST_YES=1 ST_UPDATE=1 \
      bash "$tmp/install.sh"
  ) || die "could not install st $latest"
  printf 'st: updated %s -> %s\n' "$ST_VERSION" "$latest"
}

cmd_version() { printf "st %s\n" "$ST_VERSION"; }

cmd_uninstall() {
  [ $# -eq 0 ] || die "usage: st uninstall"
  local self confdir tmux_conf fragment config staged sessions sess manifest key binding module_dir
  local fragment_keys=''
  local had_fragment=0 current_sess='' close_current=0
  self=$(st_executable)
  [ -L "$self" ] || [ "$ST_VERSION" != dev ] ||
    die "run the installed st command, not the development source file"
  [ "$(basename "$self")" = st ] || die "unexpected executable path: $self"
  manifest="$self.install"
  confdir=${ST_CONFDIR:-}
  [ -n "$confdir" ] || [ ! -f "$manifest" ] || confdir=$(sed -n '1p' "$manifest")
  confdir=${confdir:-$HOME/.config/supertree}
  tmux_conf=$(installed_tmux_conf)
  fragment="$confdir/supertree.conf"
  if [ -f "$fragment" ]; then
    had_fragment=1
    fragment_keys=$(awk '$1 == "bind" && $2 == "-n" { print $3 }' "$fragment")
  fi
  config=${ST_CONFIG_FROM_ENV:-$confdir/config}
  if [ -d "$ST_STATE" ]; then
    local state_real
    state_real=$(cd "$ST_STATE" && pwd -P)
    [ "$state_real" != / ] && [ "$state_real" != "$HOME" ] ||
      die "unsafe state directory: $ST_STATE"
  fi
  printf 'will uninstall:\n  command  %s\n  bindings %s\n  config   %s\n  state    %s\n' \
    "$self" "$fragment" "$config" "$ST_STATE" >&2
  [ -f "$tmux_conf" ] && printf '  tmux     %s (Supertree source line)\n' "$tmux_conf" >&2
  ui_ask 'proceed?' n || die "aborted"

  sessions=$(known_sessions)
  if home_is_open; then sessions=$(printf '%s\n%s' "$sessions" "$ST_HOME_SESSION"); fi
  [ -z "${TMUX:-}" ] || current_sess=$(current_session)
  while IFS= read -r sess; do
    [ -n "$sess" ] || continue
    evacuate_clients "$sess"
    if [ "$sess" = "$current_sess" ]; then close_current=1; continue; fi
    tmux kill-session -t "=$sess" 2>/dev/null || true
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
    for key in M-w M-e M-Tab M-r M-1 M-2 M-3 M-4 M-5 M-6 M-7 M-8 M-9 M-q M-s M-B M-j M-k M-v M-V M-/ $fragment_keys; do
      binding=$(tmux list-keys -T root "$key" 2>/dev/null || true)
      case $binding in *"$self"*) tmux unbind-key -n "$key" 2>/dev/null || true;; esac
    done
    if [ "$had_fragment" = 1 ]; then
      for key in M-q M-Q $fragment_keys; do
        binding=$(tmux list-keys -T root "$key" 2>/dev/null || true)
        case $binding in *'detach-client'*) tmux unbind-key -n "$key" 2>/dev/null || true;; esac
      done
      for key in M-n M-p M-h M-l M-x $fragment_keys; do
        binding=$(tmux list-keys -T root "$key" 2>/dev/null || true)
        case $binding in *'next-window'*|*'previous-window'*|*'select-pane -L'*|*'select-pane -R'*|*'kill-pane'*) tmux unbind-key -n "$key" 2>/dev/null || true;; esac
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
  ui_ok uninstalled st
  if [ "$close_current" = 1 ]; then
    tmux kill-session -t "=$current_sess" 2>/dev/null || true
  fi
}


st_register_command 'update' cmd_update 'install the latest release'
st_register_command 'uninstall' cmd_uninstall 'remove the command, config, bindings, and state'
st_register_command 'doctor' cmd_doctor 'check the installation'
cmd_binding_audit() { audit_bindings "$(installed_fragment)" "${1:-doctor}"; }
st_register_command '_binding-audit' cmd_binding_audit
st_register_command 'version' cmd_version 'show the installed version'
