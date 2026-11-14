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

# Minimums per decision 6 in the redesign plan, from the fzf CHANGELOG and tmux CHANGES:
# https://github.com/junegunn/fzf/blob/master/CHANGELOG.md  https://github.com/tmux/tmux/blob/master/CHANGES
doctor_versions() {
  local v
  if command -v fzf >/dev/null 2>&1; then
    v=$(fzf_version)
    if [ -z "$v" ]; then printf '  WARN  fzf version unreadable; the picker needs 0.45 or newer\n'
    elif version_at_least "$v" 0 45; then printf '  ok    fzf %s.%s (0.45 or newer)\n' "${v% *}" "${v#* }"
    else printf '  MISS  fzf %s.%s is too old; the picker needs 0.45 or newer\n' "${v% *}" "${v#* }"; return 1; fi
  fi
  command -v tmux >/dev/null 2>&1 || return 0
  v=$(tmux -V 2>/dev/null | awk '{ v = $2; sub(/^next-/, "", v); split(v, p, /[^0-9]+/); if (p[1] != "") print p[1] " " p[2] + 0 }')
  if [ -z "$v" ]; then printf '  WARN  tmux version unreadable; supertree needs 3.3 or newer\n'
  elif version_at_least "$v" 3 3; then printf '  ok    tmux %s.%s (3.3 or newer)\n' "${v% *}" "${v#* }"
  else printf '  MISS  tmux %s.%s is too old; supertree needs 3.3 or newer\n' "${v% *}" "${v#* }"; return 1; fi
}

dep_fix() {
  local pkg=$1
  case $1 in
    nvim) pkg=neovim;;
    claude|codex|opencode) printf '  fix   install %s, or pick another agent with st config\n' "$1"; return 0;;
  esac
  if command -v brew >/dev/null 2>&1; then printf '  fix   brew install %s\n' "$pkg"
  else printf '  fix   install %s with your package manager\n' "$pkg"; fi
}

# Piped output keeps the plain "ok / MISS / WARN / --" lines scripts and tests read.
cmd_doctor() {
  local out rc=0
  [ -t 1 ] || { doctor_checks; return; }
  out=$(doctor_checks) || rc=$?
  printf '%s\n' "$out" | awk -v okc="$UI_OK" -v errc="$UI_ERR" -v warnc="$UI_WARN" -v dim="$UI_DIM" \
    -v a="$UI_A" -v r="$UI_R" -v gok="$G_OK" -v gerr="$G_ERR" -v gwarn="$G_WARN" '
    /^  ok    / { print "  " okc gok r "  " substr($0, 9); next }
    /^  MISS  / { print "  " errc gerr r "  " substr($0, 9); problems++; next }
    /^  WARN  / { print "  " warnc gwarn r "  " substr($0, 9); problems++; next }
    /^  fix   / { print "     " dim "fix" r "  " a substr($0, 9) r; next }
    /^  --    / { if (!info++) print ""; print "  " dim substr($0, 9) r; next }
    { print "  " dim $0 r }
    END {
      if (problems) printf "\n  %s%d problem%s%s\n", errc, problems, (problems == 1 ? "" : "s"), r
      else printf "\n  %s%s%s  everything looks fine\n", okc, gok, r
    }'
  return "$rc"
}

doctor_checks() {
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
    else printf '  MISS  %s\n' "$b"; dep_fix "$b"; ok=1; fi
  done
  doctor_versions || ok=1
  self=$(command -v st 2>/dev/null) || self=""
  if [ -n "$self" ]; then printf '  ok    st on PATH (%s)\n' "$self"
  else printf '  MISS  st not on PATH\n'; printf '  fix   add ~/.local/bin to PATH in your shell profile\n'; ok=1; fi
  tmux_conf=$(installed_tmux_conf)
  fragment=$(installed_fragment)
  if [ -f "$ST_CONFIG" ] && ! bash -n "$ST_CONFIG" >/dev/null 2>&1; then
    printf '  MISS  %s has invalid shell syntax\n' "$ST_CONFIG"
    printf '  fix   bash -n %s  shows the line\n' "$ST_CONFIG"; ok=1
  fi
  if { [ "$ST_VERSION" = dev ] && grep -qF 'supertree.conf' "$tmux_conf" 2>/dev/null; } ||
     grep -qxF "source-file $fragment" "$tmux_conf" 2>/dev/null; then
    printf '  ok    %s sources supertree.conf\n' "$tmux_conf"
  else printf '  MISS  %s does not source supertree.conf\n' "$tmux_conf"; printf '  fix   st repair\n'; ok=1; fi
  if [ "$ST_VERSION" != dev ]; then
    split_key=${ST_KEY_SPLIT:-M-v}
    if [ -f "$fragment" ] && awk -v key="$split_key" '
      index($0, "bind -n " key " ") == 1 && / split"$/ { found=1 }
      END { exit !found }
    ' "$fragment"; then
      printf '  ok    %s binds %s to split\n' "$fragment" "$split_key"
    else
      printf '  MISS  %s does not bind %s to split\n' "$fragment" "$split_key"; printf '  fix   st repair\n'; ok=1
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

# Replaces KEY=… in the config, or appends it; an empty value removes the line.
config_set() { # key value
  local key=$1 value=$2 staged
  case $value in *"'"*) die "config values cannot contain a single quote";; esac
  mkdir -p "$(dirname "$ST_CONFIG")"
  [ -f "$ST_CONFIG" ] || : > "$ST_CONFIG"
  staged=$(mktemp "$ST_CONFIG.XXXXXX")
  awk -v key="$key" -v line="${value:+$key='$value'}" '
    index($0, key "=") == 1 { if (line != "" && !done) print line; done = 1; next }
    { print }
    END { if (line != "" && !done) print line }' "$ST_CONFIG" > "$staged"
  mv "$staged" "$ST_CONFIG"
}

config_rows() {
  local windows type
  windows=$(for type in $ST_WINDOWS; do printf '%s, ' "$(window_name "$type")"; done)
  windows=${windows%, }
  printf 'agent      %s%s%s\tagent\n' "$UI_B" "$(harness_label)" "$UI_R"
  printf 'windows    %s%s%s\twindows\n' "$UI_B" "$windows" "$UI_R"
  printf 'waiting    %s%s%s  %scount on the status line%s\tstatus\n' "$UI_B" \
    "$([ "${ST_STATUS_RIGHT:-0}" = 1 ] && printf on || printf off)" "$UI_R" "$UI_DIM" "$UI_R"
  printf 'sidebar    %s%s%s\tsidebar\n' "$UI_B" \
    "$([ "${ST_SIDEBAR:-1}" = 0 ] && printf off || printf on)" "$UI_R"
}

config_pick_harness() {
  local pick
  pick=$(printf '%s\n' claude codex openrouter |
    awk -v cur="$ST_HARNESS" '{ print ($0 == cur ? "● " : "  ") $0 }' |
    fzf --height=100% --reverse --border=rounded --border-label=' Agent ' --prompt='Agent › ' \
        --pointer='▌' --header='⏎ choose  esc back' --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"}) || return 0
  pick=${pick#??}
  [ "$pick" != "$ST_HARNESS" ] || return 0
  config_set ST_HARNESS "$pick"
  ST_HARNESS=$pick
}

# Enter adds a window at the end or removes it, so the order is the order you add them.
config_pick_windows() {
  local types type pick list n
  types="agent vim shell $(compgen -v ST_TAB_ | sed 's/^ST_TAB_//' | tr '\n' ' ')"
  list=$ST_WINDOWS
  while :; do
    pick=$(for type in $types; do
      n=$(printf '%s\n' $list | grep -nxF -- "$type" | cut -d: -f1)
      printf '%s  %s%s\t%s\n' "${n:- }" "$type" "$([ "$type" != agent ] || printf ' (%s)' "$(harness_label)")" "$type"
    done | fzf --delimiter=$'\t' --with-nth=1 --height=100% --reverse --border=rounded \
        --border-label=' Windows ' --prompt='Window › ' --pointer='▌' \
        --header='⏎ add at the end / remove  esc save' --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"}) || break
    type=${pick##*$'\t'}
    if printf '%s\n' $list | grep -qxF -- "$type"; then
      list=$(printf '%s\n' $list | grep -vxF -- "$type" | tr '\n' ' ')
    else
      list="$list $type"
    fi
    list=$(printf '%s' "$list" | tr -s ' ' | sed 's/^ //; s/ $//')
  done
  [ -n "$list" ] || { ui_warn windows "at least one is needed; kept $ST_WINDOWS"; sleep 1.5; return 0; }
  [ "$list" != "$ST_WINDOWS" ] || return 0
  config_set ST_WINDOWS "$list"
  ST_WINDOWS=$list
}

cmd_config() {
  local pick changed=0 pid
  [ $# -eq 0 ] || die "usage: st config"
  if [ ! -t 0 ] || [ ! -t 1 ] || ! command -v fzf >/dev/null 2>&1; then
    printf 'agent\t%s\nwindows\t%s\nwaiting\t%s\nsidebar\t%s\nconfig\t%s\n' "$ST_HARNESS" "$ST_WINDOWS" \
      "$([ "${ST_STATUS_RIGHT:-0}" = 1 ] && printf on || printf off)" \
      "$([ "${ST_SIDEBAR:-1}" = 0 ] && printf off || printf on)" "$ST_CONFIG"
    return 0
  fi
  ui_colour_on
  if declare -F fzf_gutter_init >/dev/null; then fzf_gutter_init; fi
  while :; do
    pick=$(config_rows | fzf --ansi --delimiter=$'\t' --with-nth=1 --height=100% --reverse --no-sort \
      --border=rounded --border-label=' Settings ' --border-label-pos=3 --prompt='Setting › ' \
      --pointer='▌' --header="⏎ change  esc done   $(ui_path "$ST_CONFIG")" --header-first \
      --bind='esc:abort' ${FZF_GUTTER[@]+"${FZF_GUTTER[@]}"}) || break
    case ${pick##*$'\t'} in
      agent) config_pick_harness;;
      windows) config_pick_windows;;
      status) if [ "${ST_STATUS_RIGHT:-0}" = 1 ]; then ST_STATUS_RIGHT=''; else ST_STATUS_RIGHT=1; fi
              config_set ST_STATUS_RIGHT "$ST_STATUS_RIGHT";;
      sidebar) if [ "${ST_SIDEBAR:-1}" = 0 ]; then ST_SIDEBAR=1; config_set ST_SIDEBAR ''
               else ST_SIDEBAR=0; config_set ST_SIDEBAR 0; fi;;
    esac
    changed=1
  done
  [ "$changed" = 1 ] || return 0
  ui_ok saved "$(ui_path "$ST_CONFIG")"
  # The refresher read the old config when it started; a fresh one picks up the status line.
  pid=$(cat "${ST_SIDEBAR_DIR:-/nonexistent}/poll.lock/pid" 2>/dev/null) || pid=''
  if [ -n "$pid" ] && declare -F sidebar_poll_start >/dev/null && kill "$pid" 2>/dev/null; then
    rm -rf "$ST_SIDEBAR_DIR/poll.lock"
    sidebar_poll_start
  fi
  ui_info "open trees" "pick up agent and window changes when reopened (st down, then st go)"
}

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
    tmux set-option -gu @st_need_text 2>/dev/null || true
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
st_register_command 'config' cmd_config 'change agent, windows, status line and sidebar'
cmd_binding_audit() { audit_bindings "$(installed_fragment)" "${1:-doctor}"; }
st_register_command '_binding-audit' cmd_binding_audit
st_register_command 'version' cmd_version 'show the installed version'
