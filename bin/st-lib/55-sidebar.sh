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
  local window=$1 dir=$2 pane lock holder
  sidebar_enabled || return 0
  # M-B, the new-window hook and the refresher can all add at once; one per window at a time.
  lock="$ST_SIDEBAR_DIR/.add-${window#@}"
  mkdir -p "$ST_SIDEBAR_DIR"
  if ! mkdir "$lock" 2>/dev/null; then
    holder=$(cat "$lock/pid" 2>/dev/null) || holder=''
    [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null && return 0
    rm -rf "$lock"; mkdir "$lock" 2>/dev/null || return 0
  fi
  printf '%s\n' "$$" > "$lock/pid"
  if [ -z "$(window_sidebar "$window")" ]; then
    pane=$(tmux split-window -f -hb -d -l "$ST_SIDEBAR_WIDTH" -t "$window" -c "$dir" \
      -P -F '#{pane_id}' "$(st_self)" _sidebar) &&
      tmux set-option -p -t "$pane" @st_sidebar 1 \; set-option -p -t "$pane" @st_sidebar_code "$(st_code_id)"
  fi
  rm -rf "$lock"
}

# A release swaps bin/st; a source install edits the modules beside it. Either changes this.
st_code_id() {
  local self
  self=$(st_self)
  { readlink "$self" || true; cat "$self" "$st_lib_dir"/*.sh; } 2>/dev/null | cksum
}

# A sidebar keeps the key bindings it started with, so after an upgrade restart
# every sidebar stamped with older code. Panes from before stamping have no stamp.
sidebar_respawn_stale() {
  local code=$1 self pane
  self=$(st_self)
  tmux list-panes -a -F '#{pane_id}|#{@st_sidebar}|#{@st_sidebar_code}' 2>/dev/null |
    awk -F'|' -v c="$code" '$2 == 1 && $3 != c { print $1 }' |
    while read -r pane; do
      tmux set-option -p -t "$pane" @st_sidebar_code "$code" 2>/dev/null &&
        tmux respawn-pane -k -t "$pane" "$self" _sidebar 2>/dev/null || true
    done
}

sidebar_panes_exist() {
  tmux list-panes -a -F '#{@st_sidebar}' 2>/dev/null | grep -qx 1
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
# Also restores a sidebar closed by hand and drops a duplicate if the new-window hook
# and this refresher both added one.
sidebar_sweep() {
  local kind target sess windows
  tmux list-panes -a -F '#{window_id}|#{pane_id}|#{@st_sidebar}|#{session_name}|#{session_windows}|#{@st_tree_dir}' 2>/dev/null |
    awk -F'|' -v restore="$(sidebar_enabled && echo 1)" '
      { panes[$1]++; sess[$1] = $4; nwin[$1] = $5; tree[$1] = $6
        if ($3 == 1) { if (side[$1]++) print "extra|" $2 } }
      END { for (w in panes) {
        if (side[w] == panes[w]) print "empty|" w "|" sess[w] "|" nwin[w]
        else if (!side[w] && restore && tree[w] != "") print "restore|" w "|" tree[w]
      } }' |
    while IFS='|' read -r kind target sess windows; do
      case $kind in
        extra) tmux kill-pane -t "$target" 2>/dev/null || true;;
        restore) add_sidebar "$target" "$sess";;
        empty)
          # Last window of the tree: move clients elsewhere first, or tmux detaches them.
          # Count live: several windows of one tree can empty out in the same pass.
          [ "$(tmux display-message -p -t "$target" '#{session_windows}' 2>/dev/null)" != 1 ] || evacuate_clients "$sess"
          tmux kill-window -t "$target" 2>/dev/null || true;;
      esac
    done
}

# Called once a session's windows exist; later windows get a sidebar from the hook.
sidebar_session() {
  local sess=$1 dir=$2 self win
  self=$(st_self)
  tmux set-option -t "=$sess:" @st_tree_dir "$dir"
  for win in $(tmux list-windows -t "=$sess" -F '#{window_id}'); do
    add_sidebar "$win" "$dir"
  done
  # Pad the tree label to the sidebar's width so the tabs start above the program panes.
  # tmux 3.6 pads on the right for a positive width, the reverse of its man page.
  tmux set-option -t "=$sess:" status-left-length $((ST_SIDEBAR_WIDTH + 1))
  tmux set-option -t "=$sess:" status-left \
    "#{?@st_sidebar_off, #{@supertree_label} ,#[fg=colour245]#{p$((ST_SIDEBAR_WIDTH + 1)):#{=/$((ST_SIDEBAR_WIDTH - 1))/…: #{@supertree_label}}}#[default]}"
  tmux set-hook -t "$sess" after-new-window "run-shell \"$(tmux_cmd_word "$self" dq) _sidebar-attach '#{window_id}'\""
}

sidebar_snapshot() {
  picker_rows "$@" | awk -F '\t' '{
    n = split($1, word, " "); label = word[1]
    repo = label; sub(/\/.*/, "", repo)
    print repo "\t" substr(label, length(repo) + 2) "\t" word[n] "\t" $2 "\t" $3
  }'
}

sidebar_rows() {
  local sess cache="$ST_SIDEBAR_DIR/rows" tmp
  sess=$(current_session)
  # A brand-new tree is missing from the refresher's cache; without it the cursor
  # would start on the header instead of the current tree.
  if [ ! -f "$cache" ] || [ -e "$ST_SIDEBAR_DIR/stale" ] ||
     { [ "$sess" != "$ST_HOME_SESSION" ] &&
       ! awk -F '\t' -v s="$sess" '$5 == s { found = 1 } END { exit !found }' "$cache"; }; then
    mkdir -p "$ST_SIDEBAR_DIR"
    tmp=$(mktemp "$ST_SIDEBAR_DIR/.rows.XXXXXX")
    sidebar_snapshot > "$tmp" && mv "$tmp" "$cache"
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

# Removing a closed tree leaves the session list unchanged, so the refresher would
# keep showing it until its 30s tree reload. Drop it now and reload on the next tick.
sidebar_forget() {
  local sess=$1 cache="$ST_SIDEBAR_DIR/rows" tmp
  mkdir -p "$ST_SIDEBAR_DIR"
  : > "$ST_SIDEBAR_DIR/stale"
  [ -f "$cache" ] || return 0
  tmp=$(mktemp "$ST_SIDEBAR_DIR/.rows.XXXXXX")
  awk -F '\t' -v s="$sess" '$5 != s' "$cache" > "$tmp" && mv "$tmp" "$cache"
  sidebar_poke
}

sidebar_poke() {
  local sock pane action
  action="reload:$(printf %q "$(st_self)") _sidebar-rows"
  for sock in "$ST_SIDEBAR_DIR"/*.sock; do
    [ -e "$sock" ] || continue
    pane=${sock##*/}; pane="%${pane%.sock}"
    if ! tmux display-message -p -t "$pane" '' >/dev/null 2>&1; then rm -f "$sock"; continue; fi
    curl -s -m 1 --unix-socket "$sock" -X POST http://localhost -d "$action" >/dev/null 2>&1 || true
  done
}

# Each tree keeps its own sidebar, whose cursor otherwise stays wherever it was last moved.
sidebar_recenter() {
  local sess pane sock row rows
  rows=$(sidebar_rows)
  for sess in "$@"; do
    row=$(printf '%s\n' "$rows" | awk -F '\t' -v s="$sess" '$2 == s { print NR; exit }')
    [ -n "$row" ] || continue
    tmux list-panes -s -t "=$sess" -F '#{pane_id} #{@st_sidebar}' 2>/dev/null | awk '$2 == 1 { print $1 }' |
      while read -r pane; do
        sock="$ST_SIDEBAR_DIR/${pane#%}.sock"
        [ -S "$sock" ] || continue
        curl -s -m 1 --unix-socket "$sock" -X POST http://localhost -d "pos($row)" >/dev/null 2>&1 || true
      done
  done
}

# Mirrors each agent's state onto its tab, so the tab shows it whichever pane is focused.
sidebar_tab_states() {
  local default_name sess status win
  default_name=$(harness_window)
  printf '%s\n' "$1" | while IFS=$'\t' read -r _ _ status _ sess; do
    [ -n "$sess" ] && [ "$status" != closed ] || continue
    win=$(tmux show-options -qv -t "=$sess:" @st_agent_window 2>/dev/null) || win=''
    tmux set-option -wq -t "=$sess:${win:-$default_name}" @st_tab_state "$status" 2>/dev/null || true
  done
}

sidebar_poll_start() {
  local pid
  sidebar_enabled || return 0
  pid=$(cat "$ST_SIDEBAR_DIR/poll.lock/pid" 2>/dev/null) || pid=''
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && return 0
  # run-shell -b detaches the refresher from this pane, so closing the pane leaves it running.
  tmux run-shell -b "$(tmux_cmd_word "$(st_self)") _sidebar-poll >/dev/null 2>&1"
}

# One refresher per tmux server. The tree list (git) is slow, so it reloads only
# when the session list changes or every 30s; agent states refresh every 2s.
cmd_sidebar_poll() {
  local lock="$ST_SIDEBAR_DIR/poll.lock" pid trees='' live last_live='' ticks=0 idle=0 rows old='' tmp code
  # Long-running: under st's errexit one failed step (a tmux race, an unreadable repo)
  # would end the refresher for good. A failed tick is retried on the next one instead.
  set +e
  mkdir -p "$ST_SIDEBAR_DIR"
  if ! mkdir "$lock" 2>/dev/null; then
    pid=$(cat "$lock/pid" 2>/dev/null) || pid=''
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && return 0
    rm -rf "$lock"
    mkdir "$lock" 2>/dev/null || return 0
  fi
  printf '%s\n' "$$" > "$lock/pid"
  trap 'rm -rf "$lock"' EXIT
  code=$(st_code_id)
  while :; do
    live=$(tmux list-sessions -F '#S' 2>/dev/null) || break
    # Upgraded under us: hand over to the new code. exec keeps this pid, which
    # the new refresher would read as a live holder, so release the lock first.
    if [ "$(st_code_id)" != "$code" ]; then
      rm -rf "$lock"; trap - EXIT
      exec "$(st_self)" _sidebar-poll
    fi
    sidebar_respawn_stale "$code"
    sidebar_sweep
    sidebar_fit
    if [ "$live" != "$last_live" ] || [ $((ticks % 15)) = 0 ] || [ -e "$ST_SIDEBAR_DIR/stale" ]; then
      rm -f "$ST_SIDEBAR_DIR/stale"
      trees=$(list_trees)
      last_live=$live
    fi
    rows=$(sidebar_snapshot "$trees")
    if [ "$rows" != "$old" ]; then
      tmp=$(mktemp "$ST_SIDEBAR_DIR/.rows.XXXXXX")
      printf '%s\n' "$rows" > "$tmp"
      mv "$tmp" "$ST_SIDEBAR_DIR/rows"
      sidebar_poke
      sidebar_tab_states "$rows"
      old=$rows
    fi
    # Idle means no sidebar panes, not no sockets: a new pane exists before its socket,
    # and panes whose socket path is too long never get one.
    if sidebar_panes_exist; then idle=0; else idle=$((idle + 1)); fi
    [ "$idle" -lt 3 ] || break
    ticks=$((ticks + 1))
    sleep 2
  done
  rm -rf "$lock"
  # Sidebars added while shutting down saw this refresher still alive and did not start one.
  if tmux list-sessions >/dev/null 2>&1 && sidebar_panes_exist; then exec "$(st_self)" _sidebar-poll; fi
}

cmd_sidebar() {
  local self sock rows start
  local listen=()
  [ -n "${TMUX_PANE:-}" ] || die "st _sidebar runs inside a tmux pane"
  declare -F picker_rows >/dev/null || die "sidebar needs the picker module"
  # Long-running like the refresher: a failed row read should retry, not close the pane.
  set +e
  printf -v self %q "$(st_self)"
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
          --bind="enter:execute-silent:$self _sidebar-open {2} {3}" \
          --bind="l:execute-silent:$self _sidebar-open {2} {3}" \
          --bind="double-click:execute-silent:$self _sidebar-open {2} {3}" \
          --bind="d:execute-silent:$self _sidebar-rm {2} {3}" \
          --bind='esc:execute-silent(tmux select-pane -R),h:execute-silent(tmux select-pane -R),q:execute-silent(tmux select-pane -R)' \
          --bind="ctrl-r:reload:$self _sidebar-rows" \
          --bind="resize:execute-silent:$self _sidebar-heal" || sleep 1
  done
}

cmd_sidebar_open() {
  local sess=${1:-} dir=${2:-} tty
  case $sess in
    ''|'#'*) return 0;;
    +new)
      tty=$(tmux display-message -p -t "$TMUX_PANE" '#{client_tty}')
      tmux run-shell -b "$(tmux_cmd_word "$(st_self)") go --popup $(tmux_cmd_word "$tty")"
      return 0;;
  esac
  # Leave focus on the tab so coming back to this tree lands in the program, not the list.
  tmux select-pane -t "$TMUX_PANE" -R
  build_session "$sess" "$dir"
  attach "$sess"
}

# A popup, not this pane: the y/n prompt needs more than the sidebar's width, and
# removing the current tree kills this pane while the popup keeps running.
cmd_sidebar_rm() {
  local sess=${1:-} dir=${2:-} tty
  case $sess in ''|'#'*|+new) return 0;; esac
  tty=$(tmux display-message -p -t "$TMUX_PANE" '#{client_tty}')
  tmux display-popup -c "$tty" -E -w 70 -h 16 -T ' Remove tree ' "$(st_self)" _sidebar-rm-run "$dir"
}

cmd_sidebar_rm_run() {
  # die exits; a subshell keeps the popup open long enough to read why.
  ( remove_tree_at "${1:-}" ) && return 0
  printf 'press enter to close ' >&2
  read -r _ || true
}

cmd_sidebar_attach() {
  local win=${1:-} dir
  [ -n "$win" ] || return 0
  dir=$(tmux display-message -p -t "$win" '#{@st_tree_dir}' 2>/dev/null) || return 0
  [ -n "$dir" ] || dir=$(tmux display-message -p -t "$win" '#{pane_current_path}')
  # Windows opened with prefix c get the same tab and card look; they have no slot number.
  style_tab "$win" ''
  add_sidebar "$win" "$dir"
}

focus_content() {
  local active
  active=$(tmux display-message -p -t "=$1:" '#{pane_id} #{@st_sidebar}' 2>/dev/null) || return 0
  [ "${active#* }" = 1 ] || return 0
  tmux select-pane -t "${active%% *}" -R
}

cmd_tree_step() {
  local step=$1 cur rows next dir
  [ -n "${TMUX:-}" ] || die "st _tree-next only works inside tmux"
  [ -s "$ST_SIDEBAR_DIR/rows" ] || { mkdir -p "$ST_SIDEBAR_DIR"; sidebar_snapshot > "$ST_SIDEBAR_DIR/rows"; }
  cur=$(current_session)
  # The cache can lag a just-closed tree by a tick; only step to sessions that are live now.
  rows=$(tmux list-sessions -F '#S' 2>/dev/null | awk -F '\t' 'NR == FNR { live[$0] = 1; next }
    $3 != "closed" && ($5 in live) { print $5 "\t" $4 }' - "$ST_SIDEBAR_DIR/rows")
  next=$(printf '%s\n' "$rows" | awk -v cur="$cur" -v step="$step" '
    BEGIN { FS = "\t" }
    { s[NR] = $0; if ($1 == cur) at = NR }
    END { if (!NR) exit; if (!at) at = step > 0 ? 0 : 1; i = (at - 1 + step + NR) % NR + 1; print s[i] }')
  dir=${next#*$'\t'}; next=${next%%$'\t'*}
  [ -n "$next" ] && [ "$next" != "$cur" ] || return 0
  # Through build_session so an older session gets its tabs and sidebar on the way in.
  build_session "$next" "$dir"
  attach "$next"
}

cmd_tree_next() { cmd_tree_step 1; }
cmd_tree_prev() { cmd_tree_step -1; }

# Runs from the sidebar's own fzf on resize, so width and the refresher recover even if the refresher died.
cmd_sidebar_heal() {
  sidebar_poll_start
  [ "$(tmux display-message -p -t "$TMUX_PANE" '#{pane_width}')" = "$ST_SIDEBAR_WIDTH" ] ||
    tmux resize-pane -t "$TMUX_PANE" -x "$ST_SIDEBAR_WIDTH"
}

cmd_sidebar_focus() {
  local pane
  pane=$(window_sidebar '')
  [ -n "$pane" ] || { info "no sidebar in this window"; return 0; }
  tmux select-pane -t "$pane"
  sidebar_recenter "$(tmux display-message -p -t "$pane" '#S')"
}

cmd_sidebar_toggle() {
  local win dir pane
  [ "$ST_SIDEBAR" != 0 ] || { info "sidebar is disabled (ST_SIDEBAR=0)"; return 0; }
  if [ "$(tmux show-options -gqv @st_sidebar_off)" = 1 ]; then
    tmux set-option -gu @st_sidebar_off
    sidebar_poll_start
    tmux list-windows -a -F '#{window_id}'$'\t''#{@st_tree_dir}' | while IFS=$'\t' read -r win dir; do
      [ -z "$dir" ] || add_sidebar "$win" "$dir"
    done
    [ -z "$(window_sidebar '')" ] || cmd_sidebar_focus
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
st_register_command '_sidebar-rm' cmd_sidebar_rm ''
st_register_command '_sidebar-rm-run' cmd_sidebar_rm_run ''
st_register_command '_sidebar-focus' cmd_sidebar_focus ''
st_register_command '_sidebar-heal' cmd_sidebar_heal ''
st_register_command '_tree-next' cmd_tree_next ''
st_register_command '_tree-prev' cmd_tree_prev ''
st_register_command '_sidebar-toggle' cmd_sidebar_toggle ''
