# shellcheck shell=bash
# supertree sidebar module

ST_SIDEBAR=${ST_SIDEBAR:-1}
ST_SIDEBAR_WIDTH=${ST_SIDEBAR_WIDTH:-20}
ST_SIDEBAR_DIR="$ST_STATE/sidebar"

sidebar_enabled() {
  [ "$ST_SIDEBAR" != 0 ] || return 1
  [ "$(tmux show-options -gqv @st_sidebar_off 2>/dev/null)" != 1 ]
}

window_sidebar() {
  tmux list-panes ${1:+-t "$1"} -F '#{pane_id} #{@st_sidebar}' 2>/dev/null |
    awk '$2 == 1 { print $1; exit }'
}

add_sidebar() {
  local window=$1 dir=$2 pane
  sidebar_enabled || return 0
  [ -z "$(window_sidebar "$window")" ] || return 0
  pane=$(tmux split-window -f -hb -d -l "$ST_SIDEBAR_WIDTH" -t "$window" -c "$dir" \
    -P -F '#{pane_id}' "$(st_self)" _sidebar) || return 0
  tmux set-option -p -t "$pane" @st_sidebar 1
}

# Sessions start detached at 80 columns; tmux scales panes up on attach, so pin the width back.
sidebar_fit() {
  local scope=(-a)
  [ -z "${1:-}" ] || scope=(-s -t "=$1")
  tmux list-panes "${scope[@]}" -F '#{pane_id} #{@st_sidebar} #{pane_width}' 2>/dev/null |
    awk -v w="$ST_SIDEBAR_WIDTH" '$2 == 1 && $3 != w { print $1 }' |
    while read -r pane; do tmux resize-pane -t "$pane" -x "$ST_SIDEBAR_WIDTH" 2>/dev/null || true; done
}

# Not a pane-exited hook: tmux 3.6 runs that hook from whichever window is current, not the one that exited.
sidebar_sweep() {
  tmux list-panes -a -F '#{window_id} #{@st_sidebar}' 2>/dev/null | awk '
    { panes[$1]++; if ($2 == 1) side[$1]++ }
    END { for (w in panes) if (side[w] == panes[w]) print w }' |
    while read -r win; do tmux kill-window -t "$win" 2>/dev/null || true; done
}

# Called once a session's windows exist; later windows get a sidebar from the hook.
sidebar_session() {
  local sess=$1 dir=$2 self win
  self=$(st_self)
  tmux set-option -t "$sess" @st_tree_dir "$dir"
  for win in $(tmux list-windows -t "=$sess" -F '#{window_id}'); do
    add_sidebar "$win" "$dir"
  done
  # Pad the tree label to the sidebar's width so the tabs start above the program panes.
  # tmux 3.6 pads on the right for a positive width, the reverse of its man page.
  tmux set-option -t "$sess" status-left-length $((ST_SIDEBAR_WIDTH + 1))
  tmux set-option -t "$sess" status-left \
    "#{?@st_sidebar_off, #{@supertree_label} ,#[fg=colour245]#{p$((ST_SIDEBAR_WIDTH + 1)):#{=/$((ST_SIDEBAR_WIDTH - 1))/…: #{@supertree_label}}}#[default]}"
  tmux set-hook -t "$sess" after-new-window "run-shell \"'$self' _sidebar-attach '#{window_id}'\""
}

sidebar_snapshot() {
  picker_rows "$@" | awk -F '\t' '{
    n = split($1, word, " "); label = word[1]
    repo = label; sub(/\/.*/, "", repo)
    print repo "\t" substr(label, length(repo) + 2) "\t" word[n] "\t" $2 "\t" $3
  }'
}

sidebar_rows() {
  local sess cache="$ST_SIDEBAR_DIR/rows"
  sess=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null) || sess=''
  if [ ! -s "$cache" ]; then
    mkdir -p "$ST_SIDEBAR_DIR"
    sidebar_snapshot > "$cache"
  fi
  awk -F '\t' -v cur="$sess" '
    !($1 in seen) { seen[$1] = 1; order[++n] = $1 }
    {
      if ($3 == "running") glyph = "\033[32m●\033[0m"
      else if ($3 == "input") glyph = "\033[33m◐\033[0m"
      else if ($3 == "done") glyph = "○"
      else glyph = "\033[2m·\033[0m"
      # Last path segment only: "user/ticket-123-..." prefixes would eat the narrow column.
      name = $2; sub(/.*\//, "", name)
      if ($5 == cur) name = "\033[1;36m" name "\033[0m"
      rows[$1] = rows[$1] "  " glyph " " name "\t" $5 "\t" $4 "\t" ($5 == cur) "\n"
    }
    END {
      for (i = 1; i <= n; i++) {
        if (i > 1) printf " \t#gap%d\t\n", i
        printf "%s\t#%s\t\n%s", order[i], order[i], rows[order[i]]
      }
      printf " \t#gap\t\n\033[2m+ new\033[0m\t+new\t\n"
    }' "$cache"
}

sidebar_poke() {
  local sock pane action
  action="reload('$(st_self)' _sidebar-rows)"
  for sock in "$ST_SIDEBAR_DIR"/*.sock; do
    [ -e "$sock" ] || continue
    pane=${sock##*/}; pane="%${pane%.sock}"
    if ! tmux display-message -p -t "$pane" '' >/dev/null 2>&1; then rm -f "$sock"; continue; fi
    curl -s -m 1 --unix-socket "$sock" -X POST http://localhost -d "$action" >/dev/null 2>&1 || true
  done
}

sidebar_poll_start() {
  local pid
  pid=$(cat "$ST_SIDEBAR_DIR/poll.lock/pid" 2>/dev/null) || pid=''
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && return 0
  # run-shell -b detaches the refresher from this pane, so closing the pane leaves it running.
  tmux run-shell -b "'$(st_self)' _sidebar-poll >/dev/null 2>&1"
}

# One refresher per tmux server. The tree list (git) is slow, so it reloads only
# when the session list changes or every 30s; agent states refresh every 2s.
cmd_sidebar_poll() {
  local lock="$ST_SIDEBAR_DIR/poll.lock" pid trees='' live last_live='' ticks=0 idle=0 rows old='' tmp
  local socks=()
  mkdir -p "$ST_SIDEBAR_DIR"
  if ! mkdir "$lock" 2>/dev/null; then
    pid=$(cat "$lock/pid" 2>/dev/null) || pid=''
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && return 0
    rm -rf "$lock"
    mkdir "$lock" 2>/dev/null || return 0
  fi
  printf '%s\n' "$$" > "$lock/pid"
  trap 'rm -rf "$lock"' EXIT
  while :; do
    live=$(tmux list-sessions -F '#S' 2>/dev/null) || break
    sidebar_sweep
    sidebar_fit
    if [ "$live" != "$last_live" ] || [ $((ticks % 15)) = 0 ]; then
      trees=$(list_trees)
      last_live=$live
    fi
    rows=$(sidebar_snapshot "$trees")
    if [ "$rows" != "$old" ]; then
      tmp=$(mktemp "$ST_SIDEBAR_DIR/.rows.XXXXXX")
      printf '%s\n' "$rows" > "$tmp"
      mv "$tmp" "$ST_SIDEBAR_DIR/rows"
      sidebar_poke
      old=$rows
    fi
    socks=("$ST_SIDEBAR_DIR"/*.sock)
    if [ -e "${socks[0]}" ]; then idle=0; else idle=$((idle + 1)); fi
    [ "$idle" -lt 3 ] || break
    ticks=$((ticks + 1))
    sleep 2
  done
}

cmd_sidebar() {
  local self sock rows start
  local listen=()
  [ -n "${TMUX_PANE:-}" ] || die "st _sidebar runs inside a tmux pane"
  declare -F picker_rows >/dev/null || die "sidebar needs the picker module"
  self=$(st_self)
  mkdir -p "$ST_SIDEBAR_DIR"
  sock="$ST_SIDEBAR_DIR/${TMUX_PANE#%}.sock"
  # Unix socket paths are capped near 104 bytes; past that, skip live refresh (Ctrl-R still reloads).
  [ "${#sock}" -ge 100 ] || listen=(--listen="$sock")
  sidebar_poll_start
  # Restart after Ctrl-C so the pane never drops to an empty shell.
  while :; do
    rm -f "$sock"
    rows=$(sidebar_rows)
    start=$(printf '%s\n' "$rows" | awk -F '\t' '$4 == 1 { print NR; exit }')
    printf '%s\n' "$rows" |
      fzf --ansi --no-input --no-sort --reverse --info=hidden --no-scrollbar \
          --delimiter=$'\t' --with-nth=1 --track --id-nth=2 \
          ${listen[@]+"${listen[@]}"} --pointer='▌' --gutter=' ' --ellipsis='…' --padding=1,0,0,0 \
          --bind="load:pos(${start:-1})+unbind(load)" \
          --bind='j:down,k:up,g:first,G:last' \
          --bind="enter:execute-silent('$self' _sidebar-open {2} {3})" \
          --bind="l:execute-silent('$self' _sidebar-open {2} {3})" \
          --bind='esc:execute-silent(tmux select-pane -R),h:execute-silent(tmux select-pane -R),q:execute-silent(tmux select-pane -R)' \
          --bind="ctrl-r:reload('$self' _sidebar-rows)" || sleep 1
  done
}

cmd_sidebar_open() {
  local sess=${1:-} dir=${2:-} tty
  case $sess in
    ''|'#'*) return 0;;
    +new)
      tty=$(tmux display-message -p -t "$TMUX_PANE" '#{client_tty}')
      tmux run-shell -b "'$(st_self)' go --popup '$tty'"
      return 0;;
  esac
  # Leave focus on the tab so coming back to this tree lands in the program, not the list.
  tmux select-pane -t "$TMUX_PANE" -R
  build_session "$sess" "$dir"
  attach "$sess"
}

cmd_sidebar_attach() {
  local win=${1:-} dir
  [ -n "$win" ] || return 0
  dir=$(tmux display-message -p -t "$win" '#{@st_tree_dir}' 2>/dev/null) || return 0
  [ -n "$dir" ] || dir=$(tmux display-message -p -t "$win" '#{pane_current_path}')
  add_sidebar "$win" "$dir"
}

focus_content() {
  local active
  active=$(tmux display-message -p -t "=$1:" '#{pane_id} #{@st_sidebar}' 2>/dev/null) || return 0
  [ "${active#* }" = 1 ] || return 0
  tmux select-pane -t "${active%% *}" -R
}

cmd_tree_step() {
  local step=$1 cur rows next
  [ -n "${TMUX:-}" ] || die "st _tree-next only works inside tmux"
  [ -s "$ST_SIDEBAR_DIR/rows" ] || { mkdir -p "$ST_SIDEBAR_DIR"; sidebar_snapshot > "$ST_SIDEBAR_DIR/rows"; }
  cur=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S')
  rows=$(awk -F '\t' '$3 != "closed" { print $5 }' "$ST_SIDEBAR_DIR/rows")
  next=$(printf '%s\n' "$rows" | awk -v cur="$cur" -v step="$step" '
    { s[NR] = $0; if ($0 == cur) at = NR }
    END { if (!NR) exit; if (!at) at = step > 0 ? 0 : 1; i = (at - 1 + step + NR) % NR + 1; print s[i] }')
  [ -n "$next" ] && [ "$next" != "$cur" ] || return 0
  attach "$next"
}

cmd_tree_next() { cmd_tree_step 1; }
cmd_tree_prev() { cmd_tree_step -1; }

cmd_sidebar_focus() {
  local pane
  pane=$(window_sidebar '')
  [ -n "$pane" ] || { info "no sidebar in this window"; return 0; }
  tmux select-pane -t "$pane"
}

cmd_sidebar_toggle() {
  local win dir pane
  [ "$ST_SIDEBAR" != 0 ] || { info "sidebar is disabled (ST_SIDEBAR=0)"; return 0; }
  if [ "$(tmux show-options -gqv @st_sidebar_off)" = 1 ]; then
    tmux set-option -gu @st_sidebar_off
    tmux list-windows -a -F '#{window_id}'$'\t''#{@st_tree_dir}' | while IFS=$'\t' read -r win dir; do
      [ -n "$dir" ] && add_sidebar "$win" "$dir"
    done
    return 0
  fi
  tmux set-option -g @st_sidebar_off 1
  tmux list-panes -a -F '#{pane_id} #{@st_sidebar}' | awk '$2 == 1 { print $1 }' | while read -r pane; do
    tmux kill-pane -t "$pane"
  done
}

st_register_command '_sidebar' cmd_sidebar ''
st_register_command '_sidebar-rows' sidebar_rows ''
st_register_command '_sidebar-poll' cmd_sidebar_poll ''
st_register_command '_sidebar-open' cmd_sidebar_open ''
st_register_command '_sidebar-attach' cmd_sidebar_attach ''
st_register_command '_sidebar-focus' cmd_sidebar_focus ''
st_register_command '_tree-next' cmd_tree_next ''
st_register_command '_tree-prev' cmd_tree_prev ''
st_register_command '_sidebar-toggle' cmd_sidebar_toggle ''
