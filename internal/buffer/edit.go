package buffer

import (
	"fmt"
	"slices"
)

// Change edits a single tab's arrangement. It never declares a new terminal,
// transfers ownership, changes another tab, or starts/stops a program.
// A place into an empty tab needs only PaneID and TerminalID. Otherwise place
// and move split TargetPaneID using the supplied new SplitID, axis and ratio.
type Change struct {
	Operation    string `json:"operation"`
	TabID        string `json:"tab_id"`
	PaneID       string `json:"pane_id,omitempty"`
	TerminalID   string `json:"terminal_id,omitempty"`
	TargetPaneID string `json:"target_pane_id,omitempty"`
	SplitID      string `json:"split_id,omitempty"`
	Axis         string `json:"axis,omitempty"`
	Ratio        int    `json:"ratio,omitempty"`
	Before       bool   `json:"before,omitempty"`
}

// Edit returns an independent candidate. Callers must validate the complete
// document set and persist it with a revision check before displaying it.
// Failed edits leave the original, including nested child slices, untouched.
func Edit(original Document, change Change) (Document, error) {
	if err := Validate(original); err != nil {
		return Document{}, err
	}
	doc := cloneDocument(original)
	index := slices.IndexFunc(doc.Tabs, func(tab Tab) bool { return tab.ID == change.TabID })
	if index < 0 {
		return Document{}, fmt.Errorf("tab is missing")
	}
	tab := &doc.Tabs[index]
	allowed := Change{Operation: change.Operation, TabID: change.TabID}
	switch change.Operation {
	case "place", "move":
		allowed.PaneID, allowed.TargetPaneID, allowed.SplitID = change.PaneID, change.TargetPaneID, change.SplitID
		allowed.Axis, allowed.Ratio, allowed.Before = change.Axis, change.Ratio, change.Before
		if change.Operation == "place" {
			allowed.TerminalID = change.TerminalID
		}
	case "hide":
		allowed.PaneID = change.PaneID
	case "resize":
		allowed.SplitID, allowed.Ratio = change.SplitID, change.Ratio
	case "reverse":
		allowed.SplitID = change.SplitID
	default:
		return Document{}, fmt.Errorf("unknown tab edit operation")
	}
	if allowed != change {
		return Document{}, fmt.Errorf("tab edit contains fields for another operation")
	}

	switch change.Operation {
	case "place":
		if !validID(change.PaneID) || !slices.ContainsFunc(doc.Terminals, func(t Terminal) bool { return t.ID == change.TerminalID }) {
			return Document{}, fmt.Errorf("place requires a pane ID and an already owned terminal")
		}
		for _, current := range doc.Tabs {
			for _, node := range current.Nodes {
				if node.TerminalID == change.TerminalID {
					return Document{}, fmt.Errorf("terminal already has a placement or loan holder; merge/return it explicitly")
				}
			}
		}
		pane := Node{ID: change.PaneID, Kind: "pane", TerminalID: change.TerminalID}
		if err := placePane(tab, pane, change); err != nil {
			return Document{}, err
		}
	case "hide", "move":
		pane, err := leaf(*tab, change.PaneID)
		if err != nil {
			return Document{}, err
		}
		if change.Operation == "hide" && (pane.LoanID != "" || pane.Kind == "placeholder") {
			return Document{}, fmt.Errorf("return the active loan before hiding its pane or source holder")
		}
		if change.Operation == "move" {
			if change.PaneID == change.TargetPaneID {
				return Document{}, fmt.Errorf("move requires a different target pane")
			}
			if _, err := leaf(*tab, change.TargetPaneID); err != nil {
				return Document{}, err
			}
		}
		removePane(tab, pane.ID)
		if change.Operation == "move" {
			if err := placePane(tab, pane, change); err != nil {
				return Document{}, err
			}
		}
	case "resize", "reverse":
		i := slices.IndexFunc(tab.Nodes, func(node Node) bool { return node.ID == change.SplitID && node.Kind == "split" })
		if i < 0 {
			return Document{}, fmt.Errorf("split is missing")
		}
		if change.Operation == "resize" {
			if change.Ratio < 5 || change.Ratio > 95 {
				return Document{}, fmt.Errorf("split ratio must be between 5 and 95")
			}
			tab.Nodes[i].Ratio = change.Ratio
		} else {
			node := &tab.Nodes[i]
			node.Children[0], node.Children[1] = node.Children[1], node.Children[0]
			node.Ratio = 100 - node.Ratio
		}
	}
	if err := Validate(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func cloneDocument(original Document) Document {
	doc := original
	doc.Terminals, doc.Loans, doc.Tabs = slices.Clone(original.Terminals), slices.Clone(original.Loans), slices.Clone(original.Tabs)
	for i := range doc.Tabs {
		doc.Tabs[i].Nodes = slices.Clone(original.Tabs[i].Nodes)
		for j := range doc.Tabs[i].Nodes {
			doc.Tabs[i].Nodes[j].Children = slices.Clone(original.Tabs[i].Nodes[j].Children)
		}
	}
	return doc
}

func leaf(tab Tab, id string) (Node, error) {
	for _, node := range tab.Nodes {
		if node.ID == id && node.Kind != "split" {
			return node, nil
		}
	}
	return Node{}, fmt.Errorf("pane is missing")
}

func replaceChild(tab *Tab, oldID, newID string) {
	if tab.Root == oldID {
		tab.Root = newID
	}
	for i := range tab.Nodes {
		for j, id := range tab.Nodes[i].Children {
			if id == oldID {
				tab.Nodes[i].Children[j] = newID
			}
		}
	}
}

// The input is a validated full binary tree and id is an existing leaf.
func removePane(tab *Tab, id string) {
	parent := ""
	for _, node := range tab.Nodes {
		if node.Kind != "split" {
			continue
		}
		if slices.Contains(node.Children, id) {
			parent = node.ID
			sibling := node.Children[0]
			if sibling == id {
				sibling = node.Children[1]
			}
			replaceChild(tab, parent, sibling)
			break
		}
	}
	if tab.Root == id {
		tab.Root = ""
	}
	tab.Nodes = slices.DeleteFunc(tab.Nodes, func(node Node) bool { return node.ID == id || node.ID == parent })
}

func placePane(tab *Tab, pane Node, change Change) error {
	if tab.Root == "" {
		if change.TargetPaneID != "" || change.SplitID != "" || change.Axis != "" || change.Ratio != 0 || change.Before {
			return fmt.Errorf("empty-tab placement does not take a split or target")
		}
		tab.Root = pane.ID
		tab.Nodes = append(tab.Nodes, pane)
		return nil
	}
	if change.SplitID == pane.ID || !validID(change.SplitID) || (change.Axis != "columns" && change.Axis != "rows") || change.Ratio < 5 || change.Ratio > 95 {
		return fmt.Errorf("placement requires a new split ID, axis and ratio between 5 and 95")
	}
	if _, err := leaf(*tab, change.TargetPaneID); err != nil {
		return err
	}
	for _, node := range tab.Nodes {
		if node.ID == change.SplitID || node.ID == pane.ID {
			return fmt.Errorf("new pane/split ID already exists")
		}
	}
	children := []string{change.TargetPaneID, pane.ID}
	if change.Before {
		children[0], children[1] = children[1], children[0]
	}
	replaceChild(tab, change.TargetPaneID, change.SplitID)
	tab.Nodes = append(tab.Nodes, pane, Node{ID: change.SplitID, Kind: "split", Axis: change.Axis, Ratio: change.Ratio, Children: children})
	return nil
}
