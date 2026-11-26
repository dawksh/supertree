#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
unset NO_COLOR TMUX
export ST_THEME=dark TERM=xterm-256color COLORTERM=truecolor
. "$ROOT/bin/st-lib/05-ui.sh"
ui_colour_on

# Compare emitted RGB values with the actual xterm grayscale entries, rather
# than letting different terminal capabilities silently change the surfaces.
for role in UI_PANEL UI_SEL UI_BAR UI_TAB UI_BORDER UI_TEXT UI_DIM; do
  color_var=${role}_C
  rgb=${!color_var}
  [ "$UI_DEPTH" = rgb ] || fail 'truecolor terminal did not receive RGB'
  COLORTERM='' ui_colour_on
  index=${!color_var}
  [ "$UI_DEPTH" = 256 ] || fail '256-colour terminal did not receive indexed colours'
  [ "$index" -ge 232 ] && [ "$index" -le 255 ] || fail "$role is not neutral gray"
  level=$((8 + 10 * (index - 232)))
  printf -v expected '#%02x%02x%02x' "$level" "$level" "$level"
  [ "$rgb" = "$expected" ] || fail "$role changes between RGB and 256 colours"
  [ "${!role}" = "$(printf '\033[38;5;%sm' "$index")" ] || fail "$role emitted a different colour"
  ui_colour_on
  style_var=${role}_T
  [ "${!style_var}" = "$rgb" ] || fail "$role tmux style differs from RGB"
done
case ${UI_FZF[0]} in
  *'bg:'"$UI_PANEL_C"*'bg+:'"$UI_SEL_C"*) ;;
  *) fail 'picker does not use explicit workspace surfaces';;
esac

# Explicit opt-outs retain terminal-owned colours and unstyled output.
ST_THEME=ansi ui_colour_on
[ "$UI_DEPTH" = 16 ] || fail 'ANSI opt-out changed'
[ "$UI_PANEL_T" = default ] || fail 'ANSI mode overrode terminal background'
case ${UI_FZF[0]} in *',bg:'*) fail 'ANSI picker overrode terminal background';; esac
NO_COLOR=1 bash -c '. "$1"; [ "$UI_PANEL_T" = default ] && [ -z "$UI_A" ]' _ \
  "$ROOT/bin/st-lib/05-ui.sh" || fail 'NO_COLOR opt-out changed'
printf 'ok: matching RGB/256 neutral surfaces, explicit picker colours, and ANSI/NO_COLOR opt-outs\n'
