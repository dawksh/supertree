package workspace

// View describes a connected control client using semantic IDs. Terminal focus
// remains local to the human client; selecting a view never selects a tmux pane.
type View struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	ProcessID   string   `json:"process_id"`
	Panel       string   `json:"panel"`
	Mode        string   `json:"mode"`
	Command     string   `json:"command"`
	Input       string   `json:"input"`
	Actions     []string `json:"actions"`
	Revision    int64    `json:"revision"`
}
