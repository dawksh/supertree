#!/usr/bin/env bash
# supertree installer
#   curl -fsSL https://github.com/dawksh/supertree/releases/latest/download/install.sh | bash
#
# env overrides:
#   ST_VERSION=v0.1.0   install a specific release instead of latest
#   ST_PREFIX=~/bin     where the st binary goes        (default ~/.local/bin)
#   ST_CONFDIR=~/.conf  where the tmux fragment goes    (default ~/.config/supertree)
#   ST_NO_TMUX_CONF=1   skip touching ~/.tmux.conf
#   ST_YES=1            answer yes to every prompt (non-interactive installs)
#   ST_HARNESS=codex     choose claude, codex, or openrouter without a prompt
#   ST_WINDOWS='agent shell'  choose and order tmux windows
set -euo pipefail

PATH="$HOME/.local/bin:$HOME/.opencode/bin:$PATH"

REPO=dawksh/supertree
RELEASE_ROOT=${ST_RELEASE_ROOT:-https://github.com/$REPO}
VERSION=${ST_VERSION:-latest}
PREFIX=${ST_PREFIX:-$HOME/.local/bin}
CONFDIR=${ST_CONFDIR:-$HOME/.config/supertree}
TMUX_CONF=${TMUX_CONF:-$HOME/.tmux.conf}
CONFIG="$CONFDIR/config"
HARNESS_OVERRIDE=${ST_HARNESS:-}
WINDOWS_OVERRIDE_SET=${ST_WINDOWS+x}
WINDOWS_OVERRIDE=${ST_WINDOWS:-}

command -v curl >/dev/null 2>&1 || { printf 'st: curl is required\n' >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { printf 'st: tar is required\n' >&2; exit 1; }

if [ "$VERSION" = latest ]; then
  latest_url=$(curl -fsS -o /dev/null -w '%{redirect_url}' "$RELEASE_ROOT/releases/latest") || {
    printf 'st: could not determine the latest release\n' >&2
    exit 1
  }
  VERSION=${latest_url##*/}
fi
case $VERSION in
  ''|*[!A-Za-z0-9._-]*) printf 'st: invalid release version: %s\n' "$VERSION" >&2; exit 1;;
esac
BASE="$RELEASE_ROOT/releases/download/$VERSION"

# ----------------------------------------------------------------- output

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  B=$(printf '\033[1m'); D=$(printf '\033[2m'); R=$(printf '\033[0m')
  GRN=$(printf '\033[32m'); RED=$(printf '\033[31m'); YLW=$(printf '\033[33m')
  CYN=$(printf '\033[36m')
else
  B=""; D=""; R=""; GRN=""; RED=""; YLW=""; CYN=""
fi

WIDTH=56
# progress lines overwrite themselves on a terminal, stack up in a pipe or log
if [ -t 1 ]; then CLEAR='\033[2K\r'; else CLEAR='\n'; fi
rule() {
  local i=0
  printf '  %s' "$D"
  while [ "$i" -lt "$WIDTH" ]; do printf '─'; i=$((i + 1)); done
  printf '%s\n' "$R"
}
title() {
  printf '\n%s%s' "$CYN" "$B"
  printf '%s\n' \
    '   ___  _   _  ___  ___  ___  _____  ___  ___  ___' \
    '  / __|| | | || _ \| __|| _ \|_   _|| _ \| __|| __|' \
    '  \__ \| |_| ||  _/| _| |   /  | |  |   /| _| | _|' \
    '  |___/ \___/ |_|  |___||_|_\  |_|  |_|_\|___||___|'
  printf '%s\n' "$R"
  printf '  %sworktrees × tmux × coding agents%s   %sinstaller %s%s\n' "$D" "$R" "$D" "$VERSION" "$R"
  rule
}
step() { # number, title, description
  printf '\n  %s%s%s  %s%s%s\n' "$CYN" "$1" "$R" "$B" "$2" "$R"
  [ -z "${3:-}" ] || printf '      %s%s%s\n' "$D" "$3" "$R"
  printf '\n'
}
ok()   { printf '      %s✔%s  %s\n' "$GRN" "$R" "$1"; }
bad()  { printf '      %s✘%s  %s\n' "$RED" "$R" "$1"; }
warn() { printf '      %s!%s  %s\n' "$YLW" "$R" "$1"; }
die()  { printf '\n      %s✘ %s%s\n\n' "$RED" "$1" "$R" >&2; exit 1; }

row() { # name, status glyph+color, detail
  printf '      %s  %-10s %s%s%s\n' "$2" "$1" "$D" "$3" "$R"
}

ask() { # question -> 0 yes, 1 no
  [ "${ST_YES:-0}" = 1 ] && return 0
  [ -r /dev/tty ] || { warn "no terminal to ask on; skipping"; return 1; }
  local a
  printf '\n      %s %s[Y/n]%s ' "$1" "$D" "$R"
  # a failed read means no one is there to answer — never take that as consent
  if ! { read -r a < /dev/tty; } 2>/dev/null; then
    printf '\n'; warn "no terminal to ask on; skipping"; return 1
  fi
  case ${a:-y} in y|Y|yes|YES) return 0;; *) return 1;; esac
}

# --------------------------------------------------------------- harness

harness_valid() {
  case $1 in *[!A-Za-z0-9_-]*|'') return 1;; *) return 0;; esac
}

harness_bin() {
  case $1 in
    claude) echo claude;;
    codex) echo codex;;
    openrouter) echo opencode;;
    *) echo "";;
  esac
}

harness_label() {
  case $1 in
    claude) echo "Claude Code";;
    codex) echo "Codex";;
    openrouter) echo "OpenRouter via OpenCode";;
    *) echo "$1 (custom)";;
  esac
}

select_harness() {
  local configured="" choice=""
  CONFIG_HARNESS_NEEDS_WRITE=0
  if [ -n "$HARNESS_OVERRIDE" ]; then
    harness_valid "$HARNESS_OVERRIDE" || die "invalid ST_HARNESS: $HARNESS_OVERRIDE"
    HARNESS=$HARNESS_OVERRIDE
    CONFIG_HARNESS_NEEDS_WRITE=1
    return
  fi
  if [ -f "$CONFIG" ]; then
    configured=$( ( unset ST_HARNESS; . "$CONFIG"; printf '%s' "${ST_HARNESS:-}" ) )
    if harness_valid "$configured"; then
      HARNESS=$configured
      return
    fi
  fi
  if [ -r /dev/tty ] && [ "${ST_YES:-0}" != 1 ]; then
    printf '      1  Claude Code %s(default)%s\n' "$D" "$R"
    printf '      2  Codex\n'
    printf '      3  OpenRouter %s(via OpenCode)%s\n' "$D" "$R"
    printf '\n      choose harness %s[1]%s ' "$D" "$R"
    read -r choice < /dev/tty || choice=""
  fi
  case ${choice:-1} in
    1|claude) HARNESS=claude;;
    2|codex) HARNESS=codex;;
    3|openrouter) HARNESS=openrouter;;
    *) die "unknown harness choice: $choice";;
  esac
  CONFIG_HARNESS_NEEDS_WRITE=1
}

windows_valid() {
  local type seen=' ' LC_ALL=C
  [ -n "$1" ] || return 1
  for type in $1; do
    # Custom tab names need ST_TAB_<name>; st checks that when it reads the config.
    case $type in *[!A-Za-z0-9_]*) return 1;; esac
    case $seen in *" $type "*) return 1;; esac
    seen="$seen$type "
  done
}

windows_include() {
  case " $WINDOWS " in *" $1 "*) return 0;; *) return 1;; esac
}

select_windows() {
  local configured=""
  CONFIG_WINDOWS_NEEDS_WRITE=0
  if [ "$WINDOWS_OVERRIDE_SET" = x ]; then
    windows_valid "$WINDOWS_OVERRIDE" || die "invalid ST_WINDOWS: $WINDOWS_OVERRIDE"
    WINDOWS=$WINDOWS_OVERRIDE
    CONFIG_WINDOWS_NEEDS_WRITE=1
    return
  fi
  if [ -f "$CONFIG" ]; then
    configured=$( ( unset ST_WINDOWS; . "$CONFIG"; printf '%s' "${ST_WINDOWS-__ST_UNSET__}" ) )
    if [ "$configured" != __ST_UNSET__ ]; then
      windows_valid "$configured" || die "invalid ST_WINDOWS in $CONFIG: $configured"
      WINDOWS=$configured
      return
    fi
  fi
  WINDOWS='agent vim shell'
  [ -f "$CONFIG" ] || CONFIG_WINDOWS_NEEDS_WRITE=1
}

write_config() {
  local staged="$tmpdir/config"
  if [ -f "$CONFIG" ] && { [ "$CONFIG_HARNESS_NEEDS_WRITE" = 1 ] || [ "$CONFIG_WINDOWS_NEEDS_WRITE" = 1 ]; }; then
    awk -v harness="$HARNESS" -v windows="$WINDOWS" \
        -v write_harness="$CONFIG_HARNESS_NEEDS_WRITE" -v write_windows="$CONFIG_WINDOWS_NEEDS_WRITE" '
      BEGIN { harness_written=0; windows_written=0 }
      /^ST_HARNESS=/ && write_harness { if (!harness_written) print "ST_HARNESS=" harness; harness_written=1; next }
      /^ST_WINDOWS=/ && write_windows { if (!windows_written) print "ST_WINDOWS=\"" windows "\""; windows_written=1; next }
      { print }
      END {
        if (write_harness && !harness_written) print "ST_HARNESS=" harness
        if (write_windows && !windows_written) print "ST_WINDOWS=\"" windows "\""
      }
    ' "$CONFIG" > "$staged"
    install -m 0644 "$staged" "$CONFIG"
  elif [ ! -f "$CONFIG" ]; then
    printf '%s\n' \
      '# supertree user config — sourced as shell by st' \
      '# built-ins: claude, codex, openrouter (OpenCode connected to OpenRouter)' \
      "ST_HARNESS=$HARNESS" \
      "ST_WINDOWS='$WINDOWS'" \
      '' \
      '# Custom harness example:' \
      '# ST_HARNESS=aider' \
      "# ST_HARNESS_COMMAND='aider'" \
      "# ST_HARNESS_RESUME_COMMAND='aider --resume'" > "$CONFIG"
  fi
}

# ------------------------------------------------------------ dependencies

DEPS="tmux git fzf"

dep_why() {
  case $1 in
    tmux)   echo "required for sessions and windows";;
    git)    echo "required for worktrees";;
    fzf)    echo "tree picker (M-w)";;
    nvim)   echo "editor window (M-2)";;
    claude) echo "selected agent harness (M-1)";;
    codex) echo "selected agent harness (M-1)";;
    opencode) echo "OpenRouter agent harness (M-1)";;
  esac
}

dep_version() {
  local v
  case $1 in
    tmux) v=$(tmux -V 2>/dev/null);;                    # tmux has no --version
    nvim) v=$(nvim --version 2>/dev/null | head -1);;
    *)    v=$("$1" --version 2>/dev/null | head -1) || v="";;
  esac
  printf '%s' "$v" | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1
}

detect_pm() {
  if command -v brew >/dev/null 2>&1; then echo brew
  elif command -v apt-get >/dev/null 2>&1; then echo apt
  elif command -v dnf >/dev/null 2>&1; then echo dnf
  elif command -v pacman >/dev/null 2>&1; then echo pacman
  elif command -v apk >/dev/null 2>&1; then echo apk
  else echo none; fi
}

pkg_name() { # cmd, pm
  case $1 in
    nvim) echo neovim;;
    *)    echo "$1";;
  esac
}

sudo_if_needed() {
  [ "$(id -u)" = 0 ] && { echo ""; return; }
  command -v sudo >/dev/null 2>&1 && echo sudo || echo ""
}

install_one() { # cmd, pm, logfile
  local cmd=$1 pm=$2 log=$3 pkg S
  pkg=$(pkg_name "$cmd"); S=$(sudo_if_needed)

  # Agent harnesses ship their own installers.
  if [ "$cmd" = claude ]; then
    if [ "$pm" = brew ]; then brew install --cask claude-code >>"$log" 2>&1
    else curl -fsSL https://claude.ai/install.sh | bash >>"$log" 2>&1; fi
    return
  fi
  if [ "$cmd" = codex ]; then
    curl -fsSL https://chatgpt.com/codex/install.sh | sh >>"$log" 2>&1
    return
  fi
  if [ "$cmd" = opencode ]; then
    curl -fsSL https://opencode.ai/install | bash >>"$log" 2>&1
    return
  fi

  case $pm in
    brew)   brew install "$pkg" >>"$log" 2>&1;;
    apt)    $S apt-get update -qq >>"$log" 2>&1 && $S apt-get install -y "$pkg" >>"$log" 2>&1;;
    dnf)    $S dnf install -y "$pkg" >>"$log" 2>&1;;
    pacman) $S pacman -S --noconfirm "$pkg" >>"$log" 2>&1;;
    apk)    $S apk add "$pkg" >>"$log" 2>&1;;
    *)      return 1;;
  esac
}

# ------------------------------------------------------------------ main

tmpdir=$(mktemp -d); trap 'rm -rf "$tmpdir"' EXIT
LOG="$tmpdir/install.log"

title
step "01" "Workspace" "Choose what every new tree opens."
select_harness
select_windows
row "harness" "${GRN}✔${R}" "$(harness_label "$HARNESS")"
row "windows" "${GRN}✔${R}" "${WINDOWS// / · }"
harness_cmd=$(harness_bin "$HARNESS")
windows_include vim && DEPS="$DEPS nvim"
windows_include agent && [ -n "$harness_cmd" ] && DEPS="$DEPS $harness_cmd"

step "02" "Dependencies" "Checking the tools this workspace needs."

missing=""
for d in $DEPS; do
  if command -v "$d" >/dev/null 2>&1; then
    row "$d" "${GRN}✔${R}" "$(dep_version "$d")"
  else
    row "$d" "${RED}✘${R}" "not found — $(dep_why "$d")"
    missing="$missing $d"
  fi
done

if [ -n "$missing" ]; then
  pm=$(detect_pm)
  n=$(printf '%s' "$missing" | wc -w | tr -d ' ')
  if [ "$pm" = none ]; then
    printf '\n'
    warn "$n missing, and no supported package manager found"
    warn "install them yourself, then re-run this installer"
    [ "$(uname -s)" = Darwin ] && warn "on macOS: https://brew.sh"
  elif ask "install $n missing package(s) with $B$pm$R?"; then
    printf '\n'
    for d in $missing; do
      printf '      %s…%s installing %-8s' "$D" "$R" "$d"
      if install_one "$d" "$pm" "$LOG"; then printf "$CLEAR"; ok "installed $d"
      else printf "$CLEAR"; bad "failed $d — see log below"; fi
    done
    hash -r 2>/dev/null || true
    still=""
    for d in $missing; do command -v "$d" >/dev/null 2>&1 || still="$still $d"; done
    if [ -n "$still" ]; then
      printf '\n'; warn "still missing:$still"
      if [ -s "$LOG" ]; then
        printf '\n%s' "$D"; tail -12 "$LOG" | sed 's/^/    /'; printf '%s\n' "$R"
      fi
    fi
  else
    printf '\n'; warn "skipped — supertree installs anyway, some features stay dark"
  fi
fi

# ------------------------------------------------------------- supertree

step "03" "Install" "Putting supertree in the right places."

curl -fsSL "$BASE/st.tar.gz"     -o "$tmpdir/st.tar.gz"     || die "could not download st from $BASE"
curl -fsSL "$BASE/supertree.conf" -o "$tmpdir/supertree.conf" || die "could not download supertree.conf from $BASE"
tar -tzf "$tmpdir/st.tar.gz" | awk '
  $0 == "st" || $0 == "st-lib/" || $0 ~ /^st-lib\/[A-Za-z0-9_.-]+\.sh$/ { next }
  { exit 1 }
' || die "downloaded st archive has unexpected paths"
mkdir "$tmpdir/package"
tar -xzf "$tmpdir/st.tar.gz" -C "$tmpdir/package" || die "could not unpack st from $BASE"
head -1 "$tmpdir/package/st" | grep -q '^#!/usr/bin/env bash$' ||
  die "downloaded st does not look like a script"
[ -f "$tmpdir/package/st-lib/00-core.sh" ] || die "downloaded st is missing its modules"
for file in "$tmpdir/package"/st-lib/*.sh; do
  bash -n "$file" || die "downloaded st module has invalid shell syntax: $file"
done
bash -n "$tmpdir/package/st" || die "downloaded st has invalid shell syntax"

# Release assets keep a development placeholder so the same file can be tagged
# without a source edit. Record the resolved release in the installed command.
sed "s|^ST_VERSION='dev'$|ST_VERSION='$VERSION'|" "$tmpdir/package/st" > "$tmpdir/stamped"
grep -qx "ST_VERSION='$VERSION'" "$tmpdir/stamped" || die "downloaded st has invalid version metadata"

mkdir -p "$PREFIX" "$CONFDIR"
staged_command=$(mktemp "$PREFIX/.st.install.XXXXXX")
staged_modules=$(mktemp -d "$PREFIX/.st-lib.install.XXXXXX")
install -m 0755 "$tmpdir/stamped" "$staged_command"
cp "$tmpdir/package"/st-lib/*.sh "$staged_modules/"
previous_modules=''
if [ -e "$PREFIX/st-lib-$VERSION" ]; then
  previous_modules=$(mktemp -d "$PREFIX/.st-lib.previous.XXXXXX")
  rmdir "$previous_modules"
  mv "$PREFIX/st-lib-$VERSION" "$previous_modules"
fi
if ! mv "$staged_modules" "$PREFIX/st-lib-$VERSION"; then
  [ -z "$previous_modules" ] || mv "$previous_modules" "$PREFIX/st-lib-$VERSION"
  die "could not install st modules"
fi
if ! mv -f "$staged_command" "$PREFIX/st"; then
  rm -rf "$PREFIX/st-lib-$VERSION"
  [ -z "$previous_modules" ] || mv "$previous_modules" "$PREFIX/st-lib-$VERSION"
  die "could not install st command"
fi
[ -z "$previous_modules" ] || rm -rf "$previous_modules"
# Keep custom install paths for st uninstall.
printf '%s\n%s\n' "$CONFDIR" "$TMUX_CONF" > "$PREFIX/st.install"
row "command" "${GRN}✔${R}" "$PREFIX/st"

sed "s|~/.local/bin/st|$PREFIX/st|g" "$tmpdir/supertree.conf" > "$CONFDIR/supertree.conf"
row "bindings" "${GRN}✔${R}" "$CONFDIR/supertree.conf"

write_config
row "harness" "${GRN}✔${R}" "$(harness_label "$HARNESS") · $CONFIG"
row "windows" "${GRN}✔${R}" "${WINDOWS// / · }"

if [ "${ST_NO_TMUX_CONF:-0}" != 1 ]; then
  if [ -f "$TMUX_CONF" ] && grep -q 'supertree.conf' "$TMUX_CONF"; then
    ok "tmux config  already wired up"
  else
    [ -f "$TMUX_CONF" ] && cp "$TMUX_CONF" "$TMUX_CONF.pre-supertree"
    printf '\n# supertree\nsource-file %s/supertree.conf\n' "$CONFDIR" >> "$TMUX_CONF"
    ok "tmux config  $TMUX_CONF (backup: .pre-supertree)"
  fi
  if command -v tmux >/dev/null 2>&1 && tmux list-sessions >/dev/null 2>&1; then
    tmux source-file "$TMUX_CONF" >/dev/null 2>&1 && ok "tmux         reloaded"
  fi
fi

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) printf '\n'; warn "$PREFIX is not on your PATH"
     printf '         %sexport PATH="%s:$PATH"%s\n' "$D" "$PREFIX" "$R";;
esac

rule
step "04" "Ready" "supertree $VERSION is installed."
printf '      %sst doctor%s          verify the install\n' "$B" "$R"
printf '      %sst new <branch>%s    start from any git repo\n' "$B" "$R"
if [ "$HARNESS" = openrouter ]; then
  printf '      %sopencode%s           run /connect and select OpenRouter once\n' "$B" "$R"
fi
printf '\n      %sM-w%s picker   %sM-e%s toggle   %sM-q%s / %sM-Q%s leave tmux\n\n' "$B" "$R" "$B" "$R" "$B" "$R" "$B" "$R"
rule
printf '\n'
