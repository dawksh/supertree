package workspace

type Task struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Notes       string `json:"notes"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	Actor       string `json:"actor"`
	UpdatedAt   string `json:"updated_at"`
}

type ContextItem struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	TaskID         string `json:"task_id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	ProcessID      string `json:"process_id"`
	Text           string `json:"text"`
	SourceRevision string `json:"source_revision"`
	Stale          bool   `json:"stale"`
	Truncated      bool   `json:"truncated"`
	Revision       int64  `json:"revision"`
	Actor          string `json:"actor"`
	CapturedAt     string `json:"captured_at"`
}

type Question struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	TaskID      string   `json:"task_id"`
	AgentID     string   `json:"agent_id"`
	Kind        string   `json:"kind"`
	Prompt      string   `json:"prompt"`
	Options     []string `json:"options"`
	Status      string   `json:"status"`
	Answer      string   `json:"answer"`
	Revision    int64    `json:"revision"`
	Actor       string   `json:"actor"`
	AnsweredBy  string   `json:"answered_by"`
	CreatedAt   string   `json:"created_at"`
}

type AgentMessage struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	Actor string `json:"actor"`
}

type Agent struct {
	ID           string         `json:"id"`
	WorkspaceID  string         `json:"workspace_id"`
	TaskID       string         `json:"task_id"`
	Name         string         `json:"name"`
	Provider     string         `json:"provider"`
	ProcessID    string         `json:"process_id"`
	ThreadID     string         `json:"thread_id"`
	TurnID       string         `json:"turn_id"`
	State        string         `json:"state"`
	Model        string         `json:"model"`
	Error        string         `json:"error"`
	Capabilities []string       `json:"capabilities"`
	Messages     []AgentMessage `json:"messages"`
	Questions    []Question     `json:"questions"`
	Revision     int64          `json:"revision"`
}

type Delivery struct {
	ID              string   `json:"id"`
	AgentID         string   `json:"agent_id"`
	WorkspaceID     string   `json:"workspace_id"`
	ContextIDs      []string `json:"context_ids"`
	SourceRevisions []string `json:"source_revisions"`
	Text            string   `json:"text"`
	Status          string   `json:"status"`
	TurnID          string   `json:"turn_id"`
	Error           string   `json:"error"`
	Actor           string   `json:"actor"`
	CreatedAt       string   `json:"created_at"`
}
