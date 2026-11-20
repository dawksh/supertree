package tui

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/workspace"
	"slices"
	"strconv"
	"strings"
)

type collaborationRow struct{ Kind, ID, Label string }

func (m Model) goal() (workspace.Task, bool) {
	for i := len(m.state.Tasks) - 1; i >= 0; i-- {
		task := m.state.Tasks[i]
		if task.WorkspaceID == m.workspaceID && task.Status == "open" {
			return task, true
		}
	}
	return workspace.Task{}, false
}
func (m Model) goalID() string { t, _ := m.goal(); return t.ID }
func (m Model) native() (workspace.Agent, bool) {
	for _, a := range m.state.Agents {
		if a.WorkspaceID == m.workspaceID && a.ID == m.agentID {
			return a, true
		}
	}
	return workspace.Agent{}, false
}
func (m Model) collaborationItems() []collaborationRow {
	rows := []collaborationRow{}
	for _, a := range m.state.Agents {
		if a.WorkspaceID == m.workspaceID {
			rows = append(rows, collaborationRow{"agent", a.ID, "Agent · " + a.Name + " · " + a.State})
		}
	}
	for _, item := range m.state.Contexts {
		if item.WorkspaceID != m.workspaceID {
			continue
		}
		mark := "[ ] "
		if slices.Contains(m.contextIDs, item.ID) {
			mark = "[x] "
		}
		label := mark + item.Kind + " · " + item.Title
		if item.Stale {
			label += " · STALE"
		}
		if item.Truncated {
			label += " · truncated"
		}
		rows = append(rows, collaborationRow{"context", item.ID, label})
	}
	for _, q := range m.state.Questions {
		if q.WorkspaceID == m.workspaceID && q.Status == "open" {
			rows = append(rows, collaborationRow{"question", q.ID, "Question · " + q.Kind + " · " + q.Prompt})
		}
	}
	return rows
}
func (m *Model) normalizeCollaboration() {
	if _, ok := m.native(); !ok {
		m.agentID = ""
		for _, a := range m.state.Agents {
			if a.WorkspaceID == m.workspaceID {
				if m.agentID == "" || a.Primary {
					m.agentID = a.ID
				}
				if a.Primary {
					break
				}
			}
		}
	}
	valid := []string{}
	for _, id := range m.contextIDs {
		for _, item := range m.state.Contexts {
			if item.ID == id && item.WorkspaceID == m.workspaceID {
				valid = append(valid, id)
			}
		}
	}
	m.contextIDs = valid
	m.collabIndex = clamp(m.collabIndex, 0, max(0, len(m.collaborationItems())-1))
}
func (m Model) collabSelected() (collaborationRow, bool) {
	items := m.collaborationItems()
	if len(items) == 0 {
		return collaborationRow{}, false
	}
	return items[clamp(m.collabIndex, 0, len(items)-1)], true
}
func (m *Model) form(mode, name string, fields []string) {
	m.mode, m.pendingName, m.fields, m.field = mode, name, fields, 0
}
func (m Model) collaborationKey(key tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch key.String() {
	case "c":
		if m.panel == "collaboration" {
			m.panel = "processes"
		} else {
			m.panel = "collaboration"
		}
		return m, m.present(), true
	case "t":
		m.form("task-form", "task.create", []string{"", ""})
		return m, m.present(), true
	case "f":
		m.form("capture-form", "context.capture", []string{"", "1", "0"})
		return m, m.present(), true
	case "N":
		m.form("note-form", "context.capture", []string{"", ""})
		return m, m.present(), true
	case "d":
		return m, m.invoke("context.capture", collab.CaptureArgs{WorkspaceID: m.workspaceID, TaskID: m.goalID(), Kind: "diff"}), true
	case "o":
		if p, ok := m.selected(); ok {
			return m, m.invoke("context.capture", collab.CaptureArgs{WorkspaceID: m.workspaceID, TaskID: m.goalID(), Kind: "output", ProcessID: p.ID, Title: p.Name + " output"}), true
		}
		m.message = "Select a process with output first"
		return m, nil, true
	case "a":
		for _, a := range m.state.Agents {
			if a.WorkspaceID == m.workspaceID && a.Primary {
				m.agentID, m.panel, m.message = a.ID, "collaboration", "Using this worktree's paired agent"
				return m, m.present(), true
			}
		}
		if a, ok := m.native(); ok {
			m.panel, m.message = "collaboration", "Using "+a.Name
			return m, m.present(), true
		}
		for _, p := range m.processes() {
			if p.Kind == "agent" {
				m.message = "Use st pair run in the existing agent tab after exiting its CLI; --thread resumes its exact conversation"
				return m, nil, true
			}
		}
		name := "codex"
		for i := 2; slices.ContainsFunc(m.processes(), func(p workspace.Process) bool { return p.Name == name }); i++ {
			name = fmt.Sprintf("codex %d", i)
		}
		m.panel = "collaboration"
		return m, m.invoke("agent.start", agent.StartArgs{WorkspaceID: m.workspaceID, TaskID: m.goalID(), Name: name}), true
	case "i":
		if _, ok := m.native(); !ok {
			m.message = "Start a native agent with a, or select one in the collaboration panel"
			return m, nil, true
		}
		m.form("message-form", "agent.send", []string{"", strings.Join(m.contextIDs, ",")})
		return m, m.present(), true
	case "I":
		if a, ok := m.native(); ok {
			m.mode, m.pendingName, m.pendingArgs = "confirm", "agent.interrupt", agent.Target{AgentID: a.ID, ExpectedTurnID: a.TurnID}
		}
		return m, m.present(), true
	case "u":
		if task, ok := m.goal(); ok {
			m.mode, m.pendingName, m.pendingArgs = "confirm", "task.update", map[string]any{"task_id": task.ID, "status": "done", "expected_revision": task.Revision}
		}
		return m, m.present(), true
	}
	if m.panel != "collaboration" {
		return m, nil, false
	}
	row, exists := m.collabSelected()
	switch key.String() {
	case "up", "k", "down", "j":
		delta := 1
		if key.String() == "up" || key.String() == "k" {
			delta = -1
		}
		m.collabIndex = clamp(m.collabIndex+delta, 0, max(0, len(m.collaborationItems())-1))
		if current, ok := m.collabSelected(); ok && current.Kind == "agent" {
			m.agentID = current.ID
		}
		return m, m.present(), true
	case " ":
		if exists && row.Kind == "context" {
			if slices.Contains(m.contextIDs, row.ID) {
				m.contextIDs = slices.DeleteFunc(m.contextIDs, func(id string) bool { return id == row.ID })
			} else {
				m.contextIDs = append(m.contextIDs, row.ID)
			}
		}
		return m, m.present(), true
	case "enter":
		if !exists {
			return m, nil, true
		}
		switch row.Kind {
		case "agent":
			m.agentID = row.ID
			if a, ok := m.native(); ok {
				return m, m.invoke("process.attach", process.Target{ProcessID: a.ProcessID}), true
			}
		case "context":
			for _, item := range m.state.Contexts {
				if item.ID == row.ID {
					data, _ := json.Marshal(item)
					m.result, m.mode, m.index = string(data), "result", 0
				}
			}
		case "question":
			for _, q := range m.state.Questions {
				if q.ID == row.ID {
					m.pendingArgs = collab.AnswerArgs{QuestionID: q.ID, ExpectedRevision: q.Revision}
					m.form("answer-form", "question.answer", []string{""})
				}
			}
		}
		return m, m.present(), true
	case "r":
		if exists && row.Kind == "context" {
			for _, item := range m.state.Contexts {
				if item.ID == row.ID {
					return m, m.invoke("context.refresh", collab.ContextTarget{ContextID: row.ID, ExpectedRevision: item.Revision}), true
				}
			}
		}
		fallthrough
	case "x":
		if exists && row.Kind == "agent" {
			for _, a := range m.state.Agents {
				if a.ID == row.ID {
					for _, p := range m.state.Processes {
						if p.ID == a.ProcessID {
							m.mode, m.pendingName, m.pendingArgs = "confirm", "process.stop", process.Target{ProcessID: p.ID, ExpectedRevision: p.Revision}
							if key.String() == "r" {
								m.pendingName = "process.restart"
							}
						}
					}
				}
			}
		}
		return m, m.present(), true
	case "m":
		m.message = "Move terminals from the processes panel"
		return m, nil, true
	}
	return m, nil, false
}
func (m Model) editCollaboration(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "tab" {
		m.field = (m.field + 1) % len(m.fields)
		return m, m.present()
	}
	if key.String() == "shift+tab" {
		m.field = (m.field + len(m.fields) - 1) % len(m.fields)
		return m, m.present()
	}
	if key.String() != "enter" {
		m.fields[m.field] = editText(m.fields[m.field], key)
		return m, m.present()
	}
	if m.field < len(m.fields)-1 {
		m.field++
		return m, m.present()
	}
	var args any
	switch m.mode {
	case "task-form":
		if strings.TrimSpace(m.fields[0]) == "" {
			m.message = "A goal title is required"
			return m, nil
		}
		args = collab.TaskArgs{WorkspaceID: m.workspaceID, Title: m.fields[0], Notes: m.fields[1]}
	case "capture-form":
		start, e1 := strconv.Atoi(m.fields[1])
		end, e2 := strconv.Atoi(m.fields[2])
		if e1 != nil || e2 != nil || m.fields[0] == "" {
			m.message = "Enter a file path and numeric first/last lines (0 means end)"
			return m, nil
		}
		args = collab.CaptureArgs{WorkspaceID: m.workspaceID, TaskID: m.goalID(), Kind: "file", Path: m.fields[0], StartLine: start, EndLine: end}
	case "note-form":
		if strings.TrimSpace(m.fields[1]) == "" {
			m.message = "Note text is required"
			return m, nil
		}
		args = collab.CaptureArgs{WorkspaceID: m.workspaceID, TaskID: m.goalID(), Kind: "note", Title: m.fields[0], Text: m.fields[1]}
	case "message-form":
		if strings.TrimSpace(m.fields[0]) == "" {
			m.message = "A message is required"
			return m, nil
		}
		ids := []string{}
		for _, id := range strings.Split(m.fields[1], ",") {
			if value := strings.TrimSpace(id); value != "" {
				ids = append(ids, value)
			}
		}
		args = agent.SendArgs{AgentID: m.agentID, Message: m.fields[0], ContextIDs: ids, TaskID: m.goalID()}
	case "answer-form":
		if strings.TrimSpace(m.fields[0]) == "" {
			m.message = "Enter an explicit answer; approvals use accept or decline"
			return m, nil
		}
		answer := m.pendingArgs.(collab.AnswerArgs)
		answer.Answer = m.fields[0]
		args = answer
	}
	m.mode = "working"
	return m, m.invoke(m.pendingName, args)
}
func (m Model) collaborationView(width int) []string {
	rows := []string{accent.Render("HUMAN + AGENT")}
	if goal, ok := m.goal(); ok {
		rows = append(rows, line("Goal: "+goal.Title, width), line(goal.Notes, width))
	} else {
		rows = append(rows, muted.Render("t creates a shared goal"))
	}
	items := m.collaborationItems()
	limit := max(3, (m.height-12)/2)
	offset := clamp(m.collabIndex-limit/2, 0, max(0, len(items)-limit))
	last := min(len(items), offset+limit)
	for i := offset; i < last; i++ {
		prefix := "  "
		if i == m.collabIndex {
			prefix = "› "
		}
		text := line(prefix+items[i].Label, width)
		if i == m.collabIndex {
			text = accent.Render(text)
		}
		rows = append(rows, text)
	}
	if len(items) == 0 {
		rows = append(rows, "a starts Codex · f captures a file · N adds a note")
	}
	rows = append(rows, "", muted.Render(fmt.Sprintf("%d context item(s) marked · i sends explicitly", len(m.contextIDs))))
	preview := ""
	if row, ok := m.collabSelected(); ok {
		switch row.Kind {
		case "agent":
			for _, a := range m.state.Agents {
				if a.ID == row.ID {
					rows = append(rows, line(a.ThreadID, width))
					if a.Error != "" {
						rows = append(rows, line(a.Error, width))
					}
					for _, msg := range a.Messages {
						preview += msg.Role + ": " + msg.Text + "\n\n"
					}
				}
			}
		case "context":
			for _, item := range m.state.Contexts {
				if item.ID == row.ID {
					rows = append(rows, line(item.ID+" · "+item.CapturedAt, width))
					preview = item.Text
				}
			}
		case "question":
			for _, q := range m.state.Questions {
				if q.ID == row.ID {
					preview = q.Prompt + "\nOptions: " + strings.Join(q.Options, ", ") + "\nEnter to answer"
				}
			}
		}
	}
	lines := strings.Split(clean(preview), "\n")
	available := max(1, m.height-len(rows)-9)
	if len(lines) > available {
		lines = lines[len(lines)-available:]
	}
	for _, text := range lines {
		rows = append(rows, line(text, width))
	}
	return rows
}
func (m Model) collaborationOverlay(width, height int) string {
	title, description := "", "Tab changes field · Enter advances/submits · Esc cancels"
	var labels []string
	switch m.mode {
	case "task-form":
		title = "SHARED GOAL"
		labels = []string{"Title", "Notes"}
	case "capture-form":
		title = "CAPTURE FILE CONTEXT"
		description = "Captures the saved file. First line starts at 1; last line 0 means end."
		labels = []string{"Path", "First line", "Last line"}
	case "note-form":
		title = "SHARED NOTE"
		labels = []string{"Title", "Text"}
	case "message-form":
		title = "SEND TO NATIVE AGENT"
		description = "Only the listed context IDs are sent. Stale captures must be refreshed first."
		labels = []string{"Message", "Context IDs (comma-separated)"}
	case "answer-form":
		title = "ANSWER SHARED QUESTION"
		labels = []string{"Answer"}
		answer := m.pendingArgs.(collab.AnswerArgs)
		for _, q := range m.state.Questions {
			if q.ID == answer.QuestionID {
				description = q.Prompt + "\nOptions: " + strings.Join(q.Options, ", ")
			}
		}
	}
	rows := []string{accent.Render(title), line(description, width-6), ""}
	for i, label := range labels {
		prefix := "  "
		if i == m.field {
			prefix = "› "
		}
		rows = append(rows, line(prefix+label+": "+m.fields[i], width-6))
	}
	rows = append(rows, "", "Enter submits from the last field · Esc cancels")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("86")).Width(width).Height(height - 2).MaxHeight(height).Render(strings.Join(rows, "\n"))
}
