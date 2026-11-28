package workspace

// Process identity belongs to a workspace, independent of terminal placement.
// Environment values stay in private launch definitions, not public snapshots.
type Process struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Command         []string `json:"command"`
	EnvironmentKeys []string `json:"environment_keys"`
	Directory       string   `json:"directory"`
	PaneID          string   `json:"pane_id"`
	WindowID        string   `json:"window_id"`
	SessionID       string   `json:"session_id"`
	State           string   `json:"state"`
	ExitCode        int      `json:"exit_code"`
	Owned           bool     `json:"owned"`
	Source          string   `json:"source"`
	Actions         []string `json:"actions"`
	Revision        int64    `json:"revision"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	Error           string   `json:"error"`
}
