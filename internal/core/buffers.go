package core

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/dawksh/supertree/internal/buffer"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/store"
)

const nativeBufferKind = "native-buffer-v1"

type BufferReference struct {
	WorkspaceID string `json:"workspace_id"`
	State       string `json:"state"`
}

type SavedBuffer struct {
	Revision   int64             `json:"revision"`
	Buffer     buffer.Document   `json:"buffer"`
	References []BufferReference `json:"references"`
}

type Buffers struct {
	Buffers []SavedBuffer `json:"buffers"`
}

type BufferEdit struct {
	ExpectedRevision int64           `json:"expected_revision"`
	Buffer           buffer.Document `json:"buffer"`
}

type BufferSave struct {
	Edits []BufferEdit `json:"edits"`
}

func (c *Core) registerBuffers(r *command.Registry) error {
	if err := command.Register(r, command.Definition{Name: "buffer.inspect", Version: 1, Description: "Inspect saved native tabs and terminal placements; missing workspaces remain explicit.", Effects: []string{"read"}, Repeatable: true}, func(ctx context.Context, args InspectArgs) (Buffers, error) {
		c.refresh.Lock()
		defer c.refresh.Unlock()
		return c.inspectBuffers(ctx, args.WorkspaceID)
	}); err != nil {
		return err
	}
	return command.Register(r, command.Definition{Name: "buffer.save", Version: 1, Description: "Atomically save revision-checked native arrangements; never launch or resize a terminal.", Effects: []string{"buffer.save"}}, c.saveBuffers)
}

func (c *Core) loadBuffers(ctx context.Context) ([]SavedBuffer, error) {
	docs, err := c.store.Documents(ctx, nativeBufferKind)
	if err != nil {
		return nil, err
	}
	result := make([]SavedBuffer, 0, len(docs))
	for _, doc := range docs {
		value, err := buffer.Decode(doc.Data)
		if err != nil {
			return nil, fmt.Errorf("invalid persisted native buffer %s: %w", doc.ID, err)
		}
		if value.WorkspaceID != doc.ID || doc.Revision < 1 {
			return nil, fmt.Errorf("native buffer identity or revision mismatch")
		}
		result = append(result, SavedBuffer{Revision: doc.Revision, Buffer: value})
	}
	return result, nil
}

// inspectBuffers runs with refresh held, matching compound saves. Reference
// states describe workspace availability, not the existence of a live PTY.
func (c *Core) inspectBuffers(ctx context.Context, id string) (Buffers, error) {
	if id != "" {
		if _, err := c.workspace(id); err != nil {
			return Buffers{}, err
		}
	}
	saved, err := c.loadBuffers(ctx)
	if err != nil {
		return Buffers{}, err
	}
	states := map[string]string{}
	c.mu.Lock()
	for _, w := range c.observed {
		states[w.ID] = w.State
	}
	c.mu.Unlock()
	owners := map[string]string{}
	for _, doc := range saved {
		for _, terminal := range doc.Buffer.Terminals {
			owners[terminal.ID] = doc.Buffer.WorkspaceID
		}
	}
	result := Buffers{Buffers: []SavedBuffer{}}
	for _, doc := range saved {
		if id != "" && doc.Buffer.WorkspaceID != id {
			continue
		}
		refs := map[string]bool{doc.Buffer.WorkspaceID: true}
		for _, loan := range doc.Buffer.Loans {
			refs[loan.Source.WorkspaceID], refs[loan.Destination.WorkspaceID] = true, true
		}
		for _, tab := range doc.Buffer.Tabs {
			for _, node := range tab.Nodes {
				if owner := owners[node.TerminalID]; owner != "" {
					refs[owner] = true
				}
			}
		}
		doc.References = []BufferReference{}
		for workspaceID := range refs {
			state := states[workspaceID]
			if state == "" {
				state = "unknown"
			}
			doc.References = append(doc.References, BufferReference{workspaceID, state})
		}
		sort.Slice(doc.References, func(i, j int) bool { return doc.References[i].WorkspaceID < doc.References[j].WorkspaceID })
		result.Buffers = append(result.Buffers, doc)
	}
	if id != "" && len(result.Buffers) == 0 {
		result.Buffers = append(result.Buffers, SavedBuffer{Buffer: buffer.Document{Version: buffer.Version, WorkspaceID: id, Terminals: []buffer.Terminal{}, Tabs: []buffer.Tab{}, Loans: []buffer.Loan{}}, References: []BufferReference{{id, states[id]}}})
	}
	return result, nil
}

func (c *Core) saveBuffers(ctx context.Context, args BufferSave) (Buffers, error) {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	invalid := func(message string) (Buffers, error) {
		return Buffers{}, &command.Error{Code: "invalid_arguments", Message: message}
	}
	if len(args.Edits) == 0 || len(args.Edits) > 64 {
		return invalid("buffer save requires 1 through 64 edits")
	}
	saved, err := c.loadBuffers(ctx)
	if err != nil {
		return Buffers{}, err
	}
	candidate := map[string]buffer.Document{}
	owners := map[string]string{}
	for _, doc := range saved {
		candidate[doc.Buffer.WorkspaceID] = doc.Buffer
		for _, terminal := range doc.Buffer.Terminals {
			owners[terminal.ID] = doc.Buffer.WorkspaceID
		}
	}
	updates := make([]store.DocumentUpdate, 0, len(args.Edits))
	ids := []string{}
	for _, edit := range args.Edits {
		doc := edit.Buffer
		if slices.Contains(ids, doc.WorkspaceID) {
			return invalid("duplicate workspace edit")
		}
		if _, err := c.workspace(doc.WorkspaceID); err != nil {
			return Buffers{}, err
		}
		if err := buffer.Validate(doc); err != nil {
			return invalid(err.Error())
		}
		candidate[doc.WorkspaceID] = doc
		ids = append(ids, doc.WorkspaceID)
		updates = append(updates, store.DocumentUpdate{Kind: nativeBufferKind, ID: doc.WorkspaceID, Value: doc, ExpectedRevision: edit.ExpectedRevision})
	}
	documents := make([]buffer.Document, 0, len(candidate))
	newOwners := map[string]string{}
	for _, doc := range candidate {
		documents = append(documents, doc)
		for _, terminal := range doc.Terminals {
			newOwners[terminal.ID] = doc.WorkspaceID
		}
	}
	for id, owner := range owners {
		if newOwners[id] != owner {
			return invalid("terminal ownership cannot be removed or transferred by a layout edit")
		}
	}
	if err := buffer.ValidateSet(documents); err != nil {
		return invalid(err.Error())
	}
	if _, err := c.store.PutDocuments(ctx, updates); err != nil {
		return Buffers{}, err
	}
	c.mu.Lock()
	c.state.Sequence++
	c.state.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	origin := command.Caller(ctx)
	if origin.Actor == "" {
		origin.Actor = "system"
	}
	slices.Sort(ids)
	c.publish(Event{SchemaVersion: command.SchemaVersion, Epoch: c.state.Epoch, Sequence: c.state.Sequence, Type: "buffer.changed", CapturedAt: c.state.CapturedAt, WorkspaceIDs: ids, Actor: origin.Actor, RequestID: origin.RequestID})
	c.mu.Unlock()
	return c.inspectBuffers(ctx, "")
}
