// Package tui presents the same commands that structured agent clients invoke.
// It owns navigation and human input; workspace policy stays in the core.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/workspace"
)

type Invoke func(context.Context, string, any) (json.RawMessage, error)
type Options struct {
	Context     context.Context
	Invoke      Invoke
	Attach      func(context.Context, process.Attachment) error
	WorkspaceID string
}
type stateMsg struct {
	State    workspace.Snapshot
	Commands []command.Definition
	View     workspace.View
	Err      error
}
type actionMsg struct {
	Name string
	Data json.RawMessage
	Err  error
}
type presentMsg struct {
	View       workspace.View
	Err        error
	Generation int64
}
type outputMsg struct{ ProcessID, Text string }
type invalidateMsg struct{}
type connectionMsg struct {
	Connected bool
	Message   string
}
type tickMsg time.Time
type attachMsg struct{ Err error }

type Model struct {
	options                             Options
	state                               workspace.Snapshot
	commands                            []command.Definition
	view                                workspace.View
	width, height                       int
	workspaceID, processID, panel, mode string
	index, paletteIndex, field          int
	fields                              []string
	query, input, pendingName           string
	pendingArgs                         any
	message, preview, result            string
	connected, loading, dirty           bool
	lastPresent                         time.Time
	generation                          int64
	localSelection                      bool
	agentID                             string
	contextIDs                          []string
	collabIndex                         int
}

func New(options Options) Model {
	return Model{options: options, workspaceID: options.WorkspaceID, panel: "processes", mode: "browse", width: 100, height: 30, loading: true}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tick()) }
func tick() tea.Cmd           { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		var msg stateMsg
		data, err := m.options.Invoke(m.options.Context, "workspace.inspect", struct{}{})
		if err != nil {
			msg.Err = err
			return msg
		}
		if err := json.Unmarshal(data, &msg.State); err != nil {
			msg.Err = err
			return msg
		}
		data, err = m.options.Invoke(m.options.Context, "command.list", struct{}{})
		if err != nil {
			msg.Err = err
			return msg
		}
		var list core.CommandList
		if err := json.Unmarshal(data, &list); err != nil {
			msg.Err = err
			return msg
		}
		msg.Commands = list.Commands
		for _, view := range msg.State.Views {
			if view.ID == m.view.ID {
				msg.View = view
			}
		}
		if msg.View.ID == "" {
			data, err = m.options.Invoke(m.options.Context, "view.open", core.ViewOpen{WorkspaceID: m.workspaceID})
			if err != nil {
				msg.Err = err
				return msg
			}
			if err := json.Unmarshal(data, &msg.View); err != nil {
				msg.Err = err
			}
		}
		return msg
	}
}
func (m Model) invoke(name string, args any) tea.Cmd {
	return func() tea.Msg {
		ctx := command.WithOrigin(m.options.Context, command.Origin{Actor: "human:tui", RequestID: process.ID("request")})
		data, err := m.options.Invoke(ctx, name, args)
		return actionMsg{name, data, err}
	}
}
func (m Model) processes() []workspace.Process {
	result := []workspace.Process{}
	for _, p := range m.state.Processes {
		if p.WorkspaceID == m.workspaceID {
			result = append(result, p)
		}
	}
	return result
}
func (m Model) selected() (workspace.Process, bool) {
	for _, p := range m.processes() {
		if p.ID == m.processID {
			return p, true
		}
	}
	return workspace.Process{}, false
}
func (m *Model) normalize() {
	if !slices.ContainsFunc(m.state.Workspaces, func(w workspace.Workspace) bool { return w.ID == m.workspaceID }) {
		m.workspaceID = ""
		if len(m.state.Workspaces) > 0 {
			m.workspaceID = m.state.Workspaces[0].ID
		}
	}
	items := m.processes()
	if !slices.ContainsFunc(items, func(p workspace.Process) bool { return p.ID == m.processID }) {
		m.processID = ""
		if len(items) > 0 {
			m.processID = items[0].ID
		}
	}
	m.normalizeCollaboration()
}
func (m Model) output() tea.Cmd {
	p, ok := m.selected()
	if !ok || p.State == "missing" || p.State == "stopped" || p.State == "failed" {
		return nil
	}
	return func() tea.Msg {
		data, err := m.options.Invoke(m.options.Context, "process.output", process.OutputArgs{ProcessID: p.ID, Lines: 100})
		if err != nil {
			return outputMsg{p.ID, err.Error()}
		}
		var out process.Output
		_ = json.Unmarshal(data, &out)
		return outputMsg{p.ID, out.Text}
	}
}
func (m Model) present() tea.Cmd {
	if m.view.ID == "" {
		return nil
	}
	input := m.input
	if m.mode == "form" || strings.HasSuffix(m.mode, "-form") {
		input = strings.Join(m.fields, " | ")
	}
	for _, d := range m.commands {
		if d.Name == m.pendingName && len(d.SensitiveArguments) > 0 && (m.mode == "arguments" || m.mode == "form") {
			input = "<editing command arguments>"
		}
	}
	args := core.ViewPresent{ViewID: m.view.ID, WorkspaceID: m.workspaceID, ProcessID: m.processID, Panel: m.panel, Mode: m.mode, Command: m.pendingName, Input: input, ExpectedRevision: m.view.Revision}
	return func() tea.Msg {
		data, err := m.options.Invoke(m.options.Context, "view.present", args)
		var view workspace.View
		if err == nil {
			err = json.Unmarshal(data, &view)
		}
		return presentMsg{view, err, m.generation}
	}
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case stateMsg:
		m.loading = false
		if msg.Err != nil {
			m.connected = false
			m.message = msg.Err.Error()
			return m, nil
		}
		if m.state.Epoch == msg.State.Epoch && msg.State.Sequence < m.state.Sequence {
			return m, nil
		}
		m.state, m.commands, m.connected = msg.State, msg.Commands, true
		if msg.View.ID != "" {
			if m.view.ID == msg.View.ID && msg.View.Revision > m.view.Revision && m.mode == "browse" && !m.localSelection {
				m.workspaceID, m.processID = msg.View.WorkspaceID, msg.View.ProcessID
			}
			m.view = msg.View
		}
		m.normalize()
		return m, tea.Batch(m.output(), m.present())
	case invalidateMsg:
		if m.loading {
			m.dirty = true
			return m, nil
		}
		m.loading = true
		return m, m.load()
	case presentMsg:
		if msg.Err == nil && msg.View.ID == m.view.ID && msg.View.Revision >= m.view.Revision {
			m.view = msg.View
		}
		if msg.Err == nil && msg.Generation == m.generation {
			m.localSelection = false
		}
		if msg.Err != nil {
			return m, m.load()
		}
		return m, nil
	case connectionMsg:
		m.connected = msg.Connected
		if msg.Message != "" {
			m.message = msg.Message
		}
		return m, nil
	case outputMsg:
		if msg.ProcessID == m.processID {
			m.preview = msg.Text
		}
		return m, nil
	case actionMsg:
		m.mode = "browse"
		m.pendingName = ""
		if msg.Err != nil {
			m.message = msg.Err.Error()
			return m, tea.Batch(m.load(), m.present())
		}
		m.message = msg.Name + " completed"
		m.result = string(msg.Data)
		if msg.Name == "task.create" || msg.Name == "context.capture" || msg.Name == "agent.start" {
			m.panel = "collaboration"
		}
		if msg.Name == "agent.send" {
			var delivery workspace.Delivery
			if json.Unmarshal(msg.Data, &delivery) == nil {
				m.message = "Delivery " + delivery.Status + " · " + delivery.ID
			}
			m.mode, m.index = "result", 0
			return m, tea.Batch(m.load(), m.present())
		}
		if msg.Name == "process.attach" {
			var attachment process.Attachment
			if err := json.Unmarshal(msg.Data, &attachment); err != nil {
				m.message = err.Error()
				return m, nil
			}
			return m, tea.Exec(terminalExecution{func() error { return m.options.Attach(m.options.Context, attachment) }}, func(err error) tea.Msg { return attachMsg{err} })
		}
		for _, d := range m.commands {
			if d.Name == msg.Name && len(d.Effects) == 1 && d.Effects[0] == "read" {
				m.mode = "result"
				m.index = 0
				return m, m.present()
			}
		}
		return m, m.load()
	case attachMsg:
		if msg.Err != nil {
			m.message = msg.Err.Error()
		}
		return m, m.load()
	case tickMsg:
		commands := []tea.Cmd{tick()}
		if !m.connected || m.dirty {
			m.dirty = false
			if !m.loading {
				m.loading = true
				commands = append(commands, m.load())
			}
		}
		if time.Since(m.lastPresent) > 5*time.Second {
			m.lastPresent = time.Now()
			commands = append(commands, m.present())
		}
		commands = append(commands, m.output())
		return m, tea.Batch(commands...)
	case tea.KeyMsg:
		m.generation++
		m.localSelection = true
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.mode != "browse" {
			return m.edit(msg)
		}
		if next, cmd, handled := m.collaborationKey(msg); handled {
			return next, cmd
		}
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "tab", "shift+tab":
			if m.panel == "workspaces" {
				m.panel = "processes"
			} else {
				m.panel = "workspaces"
			}
		case "up", "k":
			m.navigate(-1)
		case "down", "j":
			m.navigate(1)
		case "n":
			m.mode = "form"
			m.fields = []string{"", "", "terminal"}
			m.field = 0
			m.pendingName = "process.start"
		case "s":
			if m.workspaceID == "" {
				m.message = "Open a Git checkout first"
				break
			}
			name := "shell"
			for i := 2; slices.ContainsFunc(m.processes(), func(p workspace.Process) bool { return p.Name == name }); i++ {
				name = fmt.Sprintf("shell %d", i)
			}
			return m, m.invoke("process.start", process.StartArgs{WorkspaceID: m.workspaceID, Name: name, Kind: "shell", Command: []string{"/bin/bash", "--noprofile", "--norc"}})
		case "x", "r":
			if p, ok := m.selected(); ok {
				m.mode = "confirm"
				m.pendingName = "process.stop"
				if msg.String() == "r" {
					m.pendingName = "process.restart"
				}
				m.pendingArgs = process.Target{ProcessID: p.ID, ExpectedRevision: p.Revision}
			}
		case "enter":
			if m.panel == "workspaces" {
				m.panel = "processes"
			} else if p, ok := m.selected(); ok {
				return m, m.invoke("process.attach", process.Target{ProcessID: p.ID})
			}
		case "m":
			if _, ok := m.selected(); ok {
				m.mode = "move"
				m.index = 0
				m.input = "horizontal"
			}
		case "p", ":":
			m.mode = "palette"
			m.query = ""
			m.paletteIndex = 0
		case "?":
			m.mode = "help"
		case "ctrl+r":
			return m, m.load()
		}
		return m, tea.Batch(m.output(), m.present())
	}
	return m, nil
}

func (m *Model) navigate(delta int) {
	if m.panel == "workspaces" {
		for i, w := range m.state.Workspaces {
			if w.ID == m.workspaceID {
				j := clamp(i+delta, 0, len(m.state.Workspaces)-1)
				m.workspaceID = m.state.Workspaces[j].ID
				m.processID = ""
				m.normalize()
				return
			}
		}
		return
	}
	items := m.processes()
	for i, p := range items {
		if p.ID == m.processID {
			m.processID = items[clamp(i+delta, 0, len(items)-1)].ID
			return
		}
	}
}
func clamp(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
func (m Model) filteredCommands() []command.Definition {
	result := []command.Definition{}
	for _, d := range m.commands {
		if strings.Contains(strings.ToLower(d.Name+" "+d.Description), strings.ToLower(m.query)) {
			result = append(result, d)
		}
	}
	return result
}
func (m Model) prefill(d command.Definition) string {
	args := map[string]any{}
	props, _ := d.ArgumentSchema["properties"].(map[string]any)
	required, _ := d.ArgumentSchema["required"].([]any)
	for _, raw := range required {
		name, _ := raw.(string)
		p, _ := props[name].(map[string]any)
		switch p["type"] {
		case "string":
			args[name] = ""
		case "array":
			args[name] = []any{}
		case "object":
			args[name] = map[string]any{}
		case "integer":
			args[name] = 0
		case "boolean":
			args[name] = false
		}
	}
	if _, ok := props["workspace_id"]; ok {
		args["workspace_id"] = m.workspaceID
	}
	if _, ok := props["process_id"]; ok {
		args["process_id"] = m.processID
	}
	data, _ := json.Marshal(args)
	return string(data)
}
func (m Model) edit(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.mode = "browse"
		m.pendingName = ""
		return m, m.present()
	}
	if strings.HasSuffix(m.mode, "-form") {
		return m.editCollaboration(key)
	}
	switch m.mode {
	case "result":
		switch key.String() {
		case "up", "k":
			m.index = max(0, m.index-1)
		case "down", "j":
			m.index++
		}
		return m, nil
	case "help":
		if key.String() == "?" || key.String() == "q" {
			m.mode = "browse"
		}
		return m, m.present()
	case "confirm":
		if key.String() == "enter" {
			m.mode = "working"
			return m, m.invoke(m.pendingName, m.pendingArgs)
		}
		return m, nil
	case "working":
		return m, nil
	case "move":
		items := []workspace.Process{}
		for _, p := range m.processes() {
			if p.ID != m.processID {
				items = append(items, p)
			}
		}
		switch key.String() {
		case "up", "k":
			m.index = clamp(m.index-1, 0, len(items))
		case "down", "j":
			m.index = clamp(m.index+1, 0, len(items))
		case "tab":
			if m.input == "horizontal" {
				m.input = "vertical"
			} else {
				m.input = "horizontal"
			}
		case "enter":
			target := ""
			if m.index > 0 {
				target = items[m.index-1].ID
			}
			p, _ := m.selected()
			m.mode = "working"
			return m, m.invoke("process.move", process.MoveArgs{ProcessID: m.processID, TargetID: target, Direction: m.input, ExpectedRevision: p.Revision})
		}
		return m, nil
	case "palette":
		switch key.String() {
		case "up":
			m.paletteIndex = clamp(m.paletteIndex-1, 0, len(m.filteredCommands())-1)
		case "down":
			m.paletteIndex = clamp(m.paletteIndex+1, 0, len(m.filteredCommands())-1)
		case "enter":
			items := m.filteredCommands()
			if len(items) > 0 {
				d := items[clamp(m.paletteIndex, 0, len(items)-1)]
				m.pendingName = d.Name
				m.input = m.prefill(d)
				m.mode = "arguments"
			}
		default:
			m.query = editText(m.query, key)
			m.paletteIndex = 0
		}
		return m, m.present()
	case "arguments":
		if key.String() == "enter" {
			var args map[string]any
			if err := json.Unmarshal([]byte(m.input), &args); err != nil || args == nil {
				m.message = "Enter a JSON object"
				return m, nil
			}
			m.pendingArgs = args
			m.mode = "confirm"
			for _, d := range m.commands {
				if d.Name == m.pendingName && len(d.Effects) == 1 && d.Effects[0] == "read" {
					m.mode = "working"
					return m, m.invoke(m.pendingName, args)
				}
			}
			return m, m.present()
		}
		m.input = editText(m.input, key)
		return m, m.present()
	case "form":
		switch key.String() {
		case "tab":
			m.field = (m.field + 1) % 3
		case "shift+tab":
			m.field = (m.field + 2) % 3
		case "enter":
			if m.field < 2 {
				m.field++
				return m, m.present()
			}
			if strings.TrimSpace(m.fields[0]) == "" || strings.TrimSpace(m.fields[1]) == "" {
				m.message = "Name and shell command are required"
				return m, nil
			}
			m.mode = "working"
			return m, m.invoke("process.start", process.StartArgs{WorkspaceID: m.workspaceID, Name: m.fields[0], Command: []string{"/bin/sh", "-lc", m.fields[1]}, Kind: m.fields[2]})
		default:
			m.fields[m.field] = editText(m.fields[m.field], key)
		}
		return m, m.present()
	}
	return m, nil
}
func editText(text string, key tea.KeyMsg) string {
	switch key.Type {
	case tea.KeyBackspace:
		rs := []rune(text)
		if len(rs) > 0 {
			return string(rs[:len(rs)-1])
		}
	case tea.KeyCtrlU:
		return ""
	case tea.KeySpace:
		if len(text) < 16000 {
			return text + " "
		}
	case tea.KeyRunes:
		for _, r := range key.Runes {
			if !unicode.IsControl(r) && len(text) < 16000 {
				text += string(r)
			}
		}
	}
	return text
}

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func line(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(clean(s), "\n", " "), max(1, width), "…")
}
func (m Model) View() string {
	if m.width < 60 || m.height < 16 {
		return fmt.Sprintf("Supertree — enlarge terminal to 60×16 (currently %d×%d). q quits.\n", m.width, m.height)
	}
	status := "connecting"
	if m.connected {
		status = "connected"
	}
	header := accent.Render(" SUPERTREE ") + muted.Render("  workspace / processes   "+status)
	if m.view.ID != "" {
		header += "  " + muted.Render(m.view.ID[:min(len(m.view.ID), 13)])
	}
	leftWidth := max(20, m.width/4)
	rightWidth := m.width - leftWidth - 5
	left := []string{accent.Render("WORKSPACES")}
	for _, w := range m.state.Workspaces {
		prefix := "  "
		if w.ID == m.workspaceID {
			prefix = "› "
		}
		text := line(prefix+w.Label, leftWidth-4)
		if w.ID == m.workspaceID {
			text = accent.Render(text)
		}
		left = append(left, text)
	}
	if len(m.state.Workspaces) == 0 {
		left = append(left, "No checkout discovered.", "Run from a Git repo.")
	}
	right := []string{accent.Render("NAMED PROCESSES")}
	for _, p := range m.processes() {
		prefix := "  "
		if p.ID == m.processID {
			prefix = "› "
		}
		text := line(fmt.Sprintf("%s%-18s %-10s %s", prefix, p.Name, p.State, p.Kind), rightWidth-4)
		if p.ID == m.processID {
			text = accent.Render(text)
		}
		right = append(right, text)
	}
	if len(m.processes()) == 0 {
		right = append(right, "No named processes yet.", "s opens a shell · n starts a program")
	}
	right = append(right, "", muted.Render("TERMINAL OUTPUT"))
	if p, ok := m.selected(); ok {
		right = append(right, line(p.ID+"  "+p.Directory, rightWidth-4))
	}
	preview := strings.Split(clean(m.preview), "\n")
	limit := max(2, m.height-len(right)-8)
	if len(preview) > limit {
		preview = preview[len(preview)-limit:]
	}
	for _, text := range preview {
		right = append(right, line(text, rightWidth-4))
	}
	if m.panel == "collaboration" {
		right = m.collaborationView(rightWidth - 4)
	}
	leftBorder, rightBorder := lipgloss.Color("238"), lipgloss.Color("238")
	if m.panel == "workspaces" {
		leftBorder = "86"
	} else {
		rightBorder = "86"
	}
	boxHeight := m.height - 7
	body := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(leftBorder).Width(leftWidth-2).Height(boxHeight).MaxHeight(boxHeight+2).Render(strings.Join(left, "\n")), " ", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(rightBorder).Width(rightWidth).Height(boxHeight).MaxHeight(boxHeight+2).Render(strings.Join(right, "\n")))
	if m.mode != "browse" {
		body = m.overlay(rightWidth+leftWidth-1, boxHeight+2)
	}
	footer := muted.Render(line("Tab switch · ↑↓/jk navigate · Enter attach · s shell · n new · x stop · r restart · m move · p palette · ? help · q quit", m.width))
	if m.panel == "collaboration" {
		footer = muted.Render(line("c processes · ↑↓ select · Space mark context · Enter inspect/answer · t goal · a agent · f file · d diff · i send · I interrupt · p palette · q quit", m.width))
	}
	return strings.Join([]string{header, body, line(m.message, m.width), footer}, "\n")
}
func (m Model) overlay(width, height int) string {
	if strings.HasSuffix(m.mode, "-form") {
		return m.collaborationOverlay(width, height)
	}
	rows := []string{}
	switch m.mode {
	case "result":
		var formatted bytes.Buffer
		if json.Indent(&formatted, []byte(m.result), "", "  ") != nil {
			formatted.WriteString(m.result)
		}
		lines := strings.Split(clean(formatted.String()), "\n")
		offset := clamp(m.index, 0, max(0, len(lines)-height+5))
		rows = append(rows, accent.Render("COMMAND RESULT"), "↑↓ scroll · Esc returns", "")
		for _, text := range lines[offset:] {
			if len(rows) >= height-2 {
				break
			}
			rows = append(rows, line(text, width-6))
		}
	case "form":
		rows = append(rows, accent.Render("START NAMED PROCESS"), "Shell commands run through /bin/sh -lc. Tab changes field; Enter launches from Kind.", "")
		for i, label := range []string{"Name", "Shell command", "Kind"} {
			prefix := "  "
			if i == m.field {
				prefix = "› "
			}
			rows = append(rows, line(prefix+label+": "+m.fields[i], width-6))
		}
	case "palette":
		rows = append(rows, accent.Render("COMMAND PALETTE"), "Search: "+line(m.query, width-12), "")
		for i, d := range m.filteredCommands() {
			if i > height-7 {
				break
			}
			prefix := "  "
			if i == m.paletteIndex {
				prefix = "› "
			}
			rows = append(rows, line(prefix+d.Name+"  "+d.Description, width-6))
		}
	case "arguments":
		rows = append(rows, accent.Render(m.pendingName), "Edit JSON arguments. Enter validates; Esc cancels. Ctrl-U clears.", "", line(m.input, width-6))
	case "confirm":
		data, _ := json.Marshal(m.pendingArgs)
		rows = append(rows, accent.Render("CONFIRM "+m.pendingName), "", line(string(data), width-6), "", "Enter runs this command · Esc cancels")
	case "working":
		rows = append(rows, accent.Render("RUNNING "+m.pendingName), "Commands run once. Reconnection never repeats a mutation.")
	case "move":
		rows = append(rows, accent.Render("MOVE PROCESS"), "Tab changes direction: "+m.input, "Enter applies · Esc cancels", "")
		names := []string{"Separate window"}
		for _, p := range m.processes() {
			if p.ID != m.processID {
				names = append(names, "Beside "+p.Name)
			}
		}
		for i, name := range names {
			prefix := "  "
			if i == m.index {
				prefix = "› "
			}
			rows = append(rows, prefix+name)
		}
	case "help":
		rows = append(rows, accent.Render("WORK TOGETHER"), "s shell · n named program · Enter attach · x stop · m move", "c opens pairing · t shared goal · u marks goal done", "a starts native Codex · i sends message + marked context · I interrupts", "f captures saved file lines · d tracked diff · o process output · N note", "Space marks context · r recaptures · Enter answers a pending question", "p invokes discovered commands · agents inspect/select semantic views", "Closing the control client leaves the core and terminals running.", "", "Esc returns · q/Ctrl-C quits from the main view")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("86")).Width(width).Height(height - 2).MaxHeight(height).Render(strings.Join(rows, "\n"))
}
