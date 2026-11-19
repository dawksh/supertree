# shellcheck shell=bash
# supertree ui module: the only place colour and glyphs are decided

# Rows need a terminal on stderr; colour also respects NO_COLOR (https://no-color.org).
# Colour is set when either stream is a terminal; plain paths never print it.
UI_ROWS=0
[ -t 2 ] && UI_ROWS=1
UI_A='' UI_OK='' UI_WARN='' UI_ERR='' UI_DIM='' UI_R=''
UI_IN='' UI_RUN='' UI_REPO='' UI_B='' UI_DEPTH=''

# Inside tmux always send full colour: tmux converts it to what each attached terminal
# can show (checked on 3.6: TERM=xterm gets 16 colours, xterm-256color the full value).
# Outside tmux, COLORTERM is the convention: https://github.com/termstandard/colors
ui_depth() {
  UI_DEPTH=16
  [ "${ST_THEME:-dark}" != ansi ] || return 0
  if [ -n "${TMUX:-}" ]; then UI_DEPTH=rgb; return 0; fi
  case ${COLORTERM:-} in truecolor|24bit) UI_DEPTH=rgb; return 0;; esac
  case ${TERM:-} in *256color*|*-direct) UI_DEPTH=256;; esac
}

ui_tone() { # variable dark-rgb dark-256 light-rgb light-256 16-colour-code [bold]
  local rgb=$2 index=$3 code
  if [ "${ST_THEME:-dark}" = light ]; then rgb=$4 index=$5; fi
  case $UI_DEPTH in
    rgb) code="38;2;$rgb";;
    256) code="38;5;$index";;
    *) code=$6;;
  esac
  printf -v "$1" '\033[%s%sm' "${7:+1;}" "$code"
}

# Exact colours, so a terminal's own theme can't repaint them. The 16-colour codes are
# the fallback and ST_THEME=ansi. Dim is lighter than the design's: here it is drawn on
# the user's background, and must stay readable on mid-dark ones.
# Also called directly for text fzf renders with --ansi, which has no terminal of its own.
ui_colour_on() {
  [ -z "${NO_COLOR:-}" ] || return 0
  ui_depth
  ui_tone UI_A    '92;207;230'  80  '14;116;144' 24  36
  ui_tone UI_IN   '255;180;84'  215 '180;83;9'   130 33 bold
  ui_tone UI_WARN '255;180;84'  215 '180;83;9'   130 33
  ui_tone UI_RUN  '115;184;255' 75  '29;78;216'  26  34
  ui_tone UI_OK   '159;220;124' 150 '21;128;61'  29  32
  ui_tone UI_ERR  '255;122;122' 210 '185;28;28'  124 31
  ui_tone UI_DIM  '139;148;166' 246 '95;107;122' 60  90
  ui_tone UI_REPO '210;166;255' 183 '126;34;206' 92  35
  UI_R=$'\033[0m' UI_B=$'\033[1m'
}
if [ -t 1 ] || [ -t 2 ]; then ui_colour_on; fi

G_OK='✓' G_ERR='✗' G_WARN='!' G_INFO='›' G_ASK='?'
# Agent states match the sidebar and tabs. done is a hollow circle, not a tick:
# it only means the agent exited, maybe after a crash.
G_IN='◐' G_RUN='●' G_DONE='○' G_OFF='·' G_MAIN='⌂' G_DIRTY='±' G_AHEAD='↑'
if [ -n "${ST_ASCII:-}" ]; then
  G_OK='+' G_ERR='x' G_INFO='>'
  G_IN='!' G_RUN='*' G_DONE='o' G_OFF='.' G_MAIN='@' G_DIRTY='~' G_AHEAD='^'
fi

# Every glyph above is one column wide, so widths below count it as 1
# instead of trusting ${#var}, which counts bytes under LANG=C.
ui_status() {
  local status=$1 width=${2:-0} glyph color pad
  case $status in
    input) glyph=$G_IN color=$UI_IN;;
    running) glyph=$G_RUN color=$UI_RUN;;
    done) glyph=$G_DONE color='';;
    *) glyph=$G_OFF color=$UI_DIM;;
  esac
  pad=$((width - 2 - ${#status}))
  [ "$pad" -gt 0 ] || pad=0
  printf '%s%s %s%s%*s' "$color" "$glyph" "$status" "$UI_R" "$pad" ''
}

ui_path() {
  case $1 in
    "$HOME"/*) printf '~%s' "${1#"$HOME"}";;
    *) printf '%s' "$1";;
  esac
}

# Not a terminal: keep the exact ':: msg' line scripts and tests already read.
ui_row() {
  local glyph=$1 color=$2 verb=$3 detail=${4:-}
  if [ "$UI_ROWS" = 0 ]; then
    printf '\033[2m::\033[0m %s\n' "$verb${detail:+ $detail}" >&2
    return 0
  fi
  if [ -z "$detail" ]; then
    printf '  %s%s%s  %s\n' "$color" "$glyph" "$UI_R" "$verb" >&2
    return 0
  fi
  printf '  %s%s%s  %-10s %s%s%s\n' "$color" "$glyph" "$UI_R" "$verb" "$UI_DIM" "$detail" "$UI_R" >&2
}

ui_info() { ui_row "$G_INFO" "$UI_DIM" "$@"; }

# Key bindings run st through run-shell, which drops stderr
# (https://man.openbsd.org/tmux#run-shell), so a message there goes to tmux's message line.
ui_toast() { # message
  if [ -n "${TMUX:-}" ] && [ ! -t 2 ] &&
     tmux display-message -d 3000 "${1//#/##}" 2>/dev/null; then
    return 0
  fi
  ui_info "$1"
}
ui_ok()   { ui_row "$G_OK" "$UI_OK" "$@"; }
ui_warn() { ui_row "$G_WARN" "$UI_WARN" "$@"; }

ui_title() { # text
  printf '\n  %s%s%s\n' "$UI_B" "$1" "$UI_R" >&2
}

# Indented lines under a row; long lists end with "… N more".
ui_list() { # max
  awk -v max="$1" -v c="$UI_DIM" -v r="$UI_R" '
    NR <= max { print "                " c $0 r }
    END { if (NR > max) print "                " c "… " NR - max " more" r }' >&2
}

# fzf redraws the moment an execute() command ends, so its messages wait for a key.
ui_hold() {
  [ -t 0 ] || return 0
  printf '\n  %sany key to go back%s' "$UI_DIM" "$UI_R" >&2
  IFS= read -rsn1 _ || true
  printf '\n' >&2
}

# Key hints on one line: pairs of key and action, e.g. ui_keys ⏎ open esc back.
ui_keys() {
  printf '\n ' >&2
  while [ $# -ge 2 ]; do
    printf ' %s%s%s %s ' "$UI_A" "$1" "$UI_R" "$2" >&2
    shift 2
  done
  printf '\n' >&2
}

# One key press; Enter reads as an empty string.
ui_key() {
  local key
  IFS= read -rsn1 key || key=$'\e'
  printf '%s' "$key"
}

ui_fail() { # message [try] [exit code]
  local message=$1 try=${2:-}
  if [ "$UI_ROWS" = 0 ]; then
    printf 'st: %s\n' "$message" >&2
    exit "${3:-1}"
  fi
  printf '  %s%s%s  %s\n' "$UI_ERR" "$G_ERR" "$UI_R" "$message" >&2
  [ -z "$try" ] || printf '     %s%-10s%s %s%s%s\n' "$UI_DIM" try "$UI_R" "$UI_A" "$try" "$UI_R" >&2
  exit "${3:-1}"
}

# Exit 4: the user said no, so a popup has nothing to show before it closes.
ui_abort() { ui_fail aborted '' 4; }

# Default follows risk: callers pass y for additive actions, n for destructive ones.
ui_ask() {
  local question=$1 default=$2 hint='[y/N]' hint_color=$UI_WARN answer
  if [ "$default" = y ]; then hint='[Y/n]'; hint_color=$UI_OK; fi
  if [ "$UI_ROWS" = 1 ]; then
    printf '  %s%s%s  %s  %s%s%s ' "$UI_A" "$G_ASK" "$UI_R" "$question" "$hint_color" "$hint" "$UI_R" >&2
  else
    printf '%s %s ' "$question" "$hint" >&2
  fi
  # A failed read means nobody is there to answer; never take that as consent.
  read -r answer || return 1
  case ${answer:-$default} in y|Y|yes) return 0;; *) return 1;; esac
}

# Irreversible actions: the user types the name; an empty line cancels.
ui_type_confirm() { # word
  local word=$1 answer
  while :; do
    if [ "$UI_ROWS" = 1 ]; then
      printf '  %s%s%s  type %s%s%s to confirm %s›%s ' "$UI_A" "$G_ASK" "$UI_R" "$UI_B" "$word" "$UI_R" "$UI_A" "$UI_R" >&2
    else
      printf 'type %s to confirm: ' "$word" >&2
    fi
    read -r answer || return 1
    [ -n "$answer" ] || return 1
    [ "$answer" != "$word" ] || return 0
    ui_warn "no match" "type it exactly, or press enter to cancel"
  done
}

# A slow step as one live row: spinner, elapsed seconds, and the last 3 output lines.
# Without a terminal the command runs as before, its output unchanged.
ui_run() { # label log-file command [args...]
  local label=$1 log=$2 pid rc=0 start=$SECONDS i=0 drawn=0 cols line
  shift 2
  if [ "$UI_ROWS" = 0 ]; then "$@"; return; fi
  local -a frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)
  [ -z "${ST_ASCII:-}" ] || frames=('|' '/' '-' '\')
  cols=$(tput cols 2>/dev/null || printf 80)
  "$@" < /dev/null > "$log" 2>&1 &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    [ "$drawn" = 0 ] || printf '\033[%dA\033[J' "$drawn" >&2
    printf '  %s%s%s  %-10s %s%ss%s\n' "$UI_A" "${frames[i % ${#frames[@]}]}" "$UI_R" "$label" "$UI_DIM" $((SECONDS - start)) "$UI_R" >&2
    drawn=1
    while IFS= read -r line; do
      printf '     %s│ %s%s\n' "$UI_DIM" "$line" "$UI_R" >&2
      drawn=$((drawn + 1))
    done < <(ui_log_tail "$log" 3 $((cols - 8)))
    i=$((i + 1))
    sleep 0.1
  done
  wait "$pid" || rc=$?
  [ "$drawn" = 0 ] || printf '\033[%dA\033[J' "$drawn" >&2
  if [ "$rc" = 0 ]; then
    ui_ok "$label" "$((SECONDS - start))s"
    return 0
  fi
  ui_row "$G_ERR" "$UI_ERR" "$label" "failed after $((SECONDS - start))s (exit $rc)"
  ui_log_tail "$log" 3 $((cols - 8)) | ui_list 3
  return "$rc"
}

# Control bytes dropped so command output can't move the cursor under the live row.
ui_log_tail() { # file lines width
  tail -n "$2" "$1" 2>/dev/null | LC_ALL=C tr -d '\000-\010\013-\037' | cut -c "1-$3"
}
