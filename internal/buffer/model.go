// Package buffer owns native tab arrangements and terminal placement. It has no
// terminal engine, subprocess, database, or client-focus dependencies.
package buffer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	Version          = 1
	MaxDocumentBytes = 64 << 10
	MaxTabs          = 32
	MaxTerminals     = 128
	MaxNodes         = 255
	MaxDepth         = 32
)

// Document is desired metadata only. Declaring a terminal does not launch it.
// Slice order is tab order; a terminal omitted from all panes is hidden.
type Document struct {
	Version     int        `json:"version"`
	WorkspaceID string     `json:"workspace_id"`
	Terminals   []Terminal `json:"terminals"`
	Tabs        []Tab      `json:"tabs"`
	Loans       []Loan     `json:"loans"`
}

type Terminal struct {
	ID string `json:"id"`
}

type Tab struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Root  string `json:"root,omitempty"`
	Nodes []Node `json:"nodes"`
}

// Nodes use explicit child IDs instead of recursive JSON. This keeps decoding
// bounded and allows cycle/depth checks before traversing a split tree.
// Axis is columns (left/right) or rows (top/bottom); Ratio is an integer percent
// assigned to the first child, from 5 through 95 inclusive.
type Node struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	TerminalID string   `json:"terminal_id,omitempty"`
	LoanID     string   `json:"loan_id,omitempty"`
	Axis       string   `json:"axis,omitempty"`
	Ratio      int      `json:"ratio,omitempty"`
	Children   []string `json:"children,omitempty"`
}

type Location struct {
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	PaneID      string `json:"pane_id"`
}

// Loan is stored by the terminal's owner. The source must be a placeholder and
// the destination its sole live placement. A local tab merge uses the same rule.
type Loan struct {
	ID          string   `json:"id"`
	TerminalID  string   `json:"terminal_id"`
	Source      Location `json:"source"`
	Destination Location `json:"destination"`
}

// Decode rejects unknown and noncanonical field names before typed decoding.
func Decode(data []byte) (Document, error) {
	if len(data) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("native buffer exceeds 64 KiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return Document{}, fmt.Errorf("native buffer must be an object")
	}
	// encoding/json accepts case-insensitive struct keys. Compare the complete
	// shape against a canonical re-encoding so those aliases are not persisted.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc Document
	if err := decoder.Decode(&doc); err != nil {
		return Document{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Document{}, fmt.Errorf("expected one native buffer")
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return Document{}, err
	}
	var actual, expected any
	_ = json.Unmarshal(data, &actual)
	_ = json.Unmarshal(canonical, &expected)
	if err := canonicalFields(actual, expected); err != nil {
		return Document{}, err
	}
	return doc, Validate(doc)
}

func canonicalFields(actual, expected any) error {
	switch a := actual.(type) {
	case map[string]any:
		e, ok := expected.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid native buffer object")
		}
		for key, value := range a {
			target, found := e[key]
			if !found {
				// Empty optional fields may be omitted by the canonical encoder.
				if value == "" && (key == "root" || key == "terminal_id" || key == "loan_id" || key == "axis") {
					continue
				}
				if key == "ratio" && value == float64(0) {
					continue
				}
				if key == "children" {
					if v, ok := value.([]any); ok && len(v) == 0 {
						continue
					}
				}
				return fmt.Errorf("unknown or noncanonical native buffer field %q", key)
			}
			if err := canonicalFields(value, target); err != nil {
				return err
			}
		}
	case []any:
		e, ok := expected.([]any)
		if !ok || len(a) != len(e) {
			return fmt.Errorf("invalid native buffer array")
		}
		for i := range a {
			if err := canonicalFields(a[i], e[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func Validate(doc Document) error {
	if doc.Version != Version || !validID(doc.WorkspaceID) {
		return fmt.Errorf("invalid native buffer version or workspace ID")
	}
	if len(doc.Tabs) > MaxTabs || len(doc.Terminals) > MaxTerminals || len(doc.Loans) > MaxTerminals {
		return fmt.Errorf("native buffer exceeds tab, terminal or loan limit")
	}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > MaxDocumentBytes {
		return fmt.Errorf("native buffer exceeds 64 KiB")
	}
	terminals, tabs, nodes, loans := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, terminal := range doc.Terminals {
		if !validID(terminal.ID) || terminals[terminal.ID] {
			return fmt.Errorf("invalid or duplicate terminal ID")
		}
		terminals[terminal.ID] = true
	}
	for _, tab := range doc.Tabs {
		if !validID(tab.ID) || tabs[tab.ID] || strings.TrimSpace(tab.Name) == "" || len(tab.Name) > 128 || strings.ContainsFunc(tab.Name, unicode.IsControl) {
			return fmt.Errorf("invalid or duplicate tab")
		}
		tabs[tab.ID] = true
		if len(nodes)+len(tab.Nodes) > MaxNodes {
			return fmt.Errorf("native buffer exceeds 255 nodes")
		}
		byID := map[string]Node{}
		for _, node := range tab.Nodes {
			if !validID(node.ID) || nodes[node.ID] {
				return fmt.Errorf("invalid or duplicate node ID")
			}
			nodes[node.ID], byID[node.ID] = true, node
			switch node.Kind {
			case "pane", "placeholder":
				if !validID(node.TerminalID) || node.Axis != "" || node.Ratio != 0 || len(node.Children) != 0 || (node.LoanID != "" && !validID(node.LoanID)) || (node.Kind == "placeholder" && node.LoanID == "") {
					return fmt.Errorf("invalid terminal pane")
				}
			case "split":
				if node.TerminalID != "" || node.LoanID != "" || (node.Axis != "rows" && node.Axis != "columns") || node.Ratio < 5 || node.Ratio > 95 || len(node.Children) != 2 {
					return fmt.Errorf("invalid split")
				}
			default:
				return fmt.Errorf("unknown native node kind")
			}
		}
		visited := map[string]bool{}
		var visit func(string, int) error
		visit = func(id string, depth int) error {
			node, exists := byID[id]
			if !exists || visited[id] || depth > MaxDepth {
				return fmt.Errorf("missing, repeated, cyclic or too-deep node")
			}
			visited[id] = true
			for _, child := range node.Children {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if tab.Root != "" {
			if err := visit(tab.Root, 1); err != nil {
				return err
			}
		}
		if len(visited) != len(byID) {
			return fmt.Errorf("unreachable native buffer nodes")
		}
	}
	for _, loan := range doc.Loans {
		if !validID(loan.ID) || loans[loan.ID] || !terminals[loan.TerminalID] || loan.Source.WorkspaceID != doc.WorkspaceID || loan.Source == loan.Destination {
			return fmt.Errorf("invalid or duplicate loan")
		}
		for _, location := range []Location{loan.Source, loan.Destination} {
			if !validID(location.WorkspaceID) || !validID(location.TabID) || !validID(location.PaneID) {
				return fmt.Errorf("invalid loan location")
			}
		}
		loans[loan.ID] = true
	}
	return nil
}

// ValidateSet checks ownership and loan receipts across the complete persisted
// set. A save cannot leave one half of a move committed or display a terminal twice.
func ValidateSet(documents []Document) error {
	owners, placed := map[string]string{}, map[string]bool{}
	nodes := map[Location]Node{}
	loans := map[string]Loan{}
	workspaces := map[string]bool{}
	for _, doc := range documents {
		if err := Validate(doc); err != nil {
			return err
		}
		if workspaces[doc.WorkspaceID] {
			return fmt.Errorf("duplicate workspace document")
		}
		workspaces[doc.WorkspaceID] = true
		for _, terminal := range doc.Terminals {
			if owners[terminal.ID] != "" {
				return fmt.Errorf("terminal has multiple owners")
			}
			owners[terminal.ID] = doc.WorkspaceID
		}
		for _, loan := range doc.Loans {
			if _, found := loans[loan.ID]; found {
				return fmt.Errorf("duplicate loan ID")
			}
			loans[loan.ID] = loan
		}
		for _, tab := range doc.Tabs {
			for _, node := range tab.Nodes {
				if node.Kind != "split" {
					nodes[Location{doc.WorkspaceID, tab.ID, node.ID}] = node
				}
			}
		}
	}
	for location, node := range nodes {
		owner := owners[node.TerminalID]
		if owner == "" {
			return fmt.Errorf("pane references an unknown terminal")
		}
		if node.Kind == "pane" {
			if placed[node.TerminalID] {
				return fmt.Errorf("terminal has multiple live placements")
			}
			placed[node.TerminalID] = true
		}
		if node.LoanID == "" {
			if owner != location.WorkspaceID {
				return fmt.Errorf("foreign terminal requires a loan")
			}
			continue
		}
		loan, exists := loans[node.LoanID]
		if !exists || loan.TerminalID != node.TerminalID || owner != loan.Source.WorkspaceID ||
			(node.Kind == "placeholder" && loan.Source != location) || (node.Kind == "pane" && loan.Destination != location) {
			return fmt.Errorf("pane does not match its loan receipt")
		}
	}
	for _, loan := range loans {
		source, sourceExists := nodes[loan.Source]
		target, targetExists := nodes[loan.Destination]
		if !sourceExists || !targetExists || source.Kind != "placeholder" || target.Kind != "pane" || source.LoanID != loan.ID || target.LoanID != loan.ID || source.TerminalID != loan.TerminalID || target.TerminalID != loan.TerminalID {
			return fmt.Errorf("loan source or destination is missing")
		}
	}
	return nil
}
