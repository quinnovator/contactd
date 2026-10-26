package core

// HelperTask is a durable helper_tasks row.
type HelperTask struct {
	TerminalSeq   uint64
	ID            string
	ContextID     string
	Contact       ContactID
	Title         string
	State         string
	StatusMessage string
	StatusTS      int64
	EveryMS       int64
	UntilTS       int64
	MaxChecks     int
	NextDue       int64
	Checks        int
	Failures      int
	LastCheckAt   int64
	LastOutcome   string
	FirstSeq      uint64
	PushedSeq     uint64
	PushedState   string
	CreatedAt     int64
}

// PushConfig is a durable helper_push_configs row.
type PushConfig struct {
	TaskID          string
	ID              string
	URL             string
	Token           string
	AuthScheme      string
	AuthCredentials string
	CreatedAt       int64
}

// RemoteHelper is a durable remote_helpers row.
type RemoteHelper struct {
	Handle          uint64
	Parent          ContactID
	AgentURL        string
	TaskID          string
	ContextID       string
	Title           string
	State           string
	StatusMessage   string
	HelperContact   ContactID
	EveryMS         int64
	UntilTS         int64
	NextCheck       int64
	Checks          int
	Reports         int
	LastCheckAt     int64
	LastReportAt    int64
	LastOutcome     string
	PushToken       string
	PushSecret      string
	CancelRequested bool
	NotifiedState   string
	CreatedAt       int64
	UpdatedAt       int64
}

// HelperTaskFilter selects task snapshots in descending creation order.
type HelperTaskFilter struct {
	ContextID string
	State     string
	After     int64 // inclusive status timestamp lower bound; zero selects all
	Cursor    string
	Limit     int
}

// HelperJSON is a requester helper snapshot, without push credentials.
type HelperJSON struct {
	Handle        uint64    `json:"handle"`
	Contact       ContactID `json:"contact,omitempty"`
	Title         string    `json:"title"`
	TaskID        string    `json:"task_id"`
	ContextID     string    `json:"context_id"`
	State         string    `json:"state"`
	StatusMessage string    `json:"status_message,omitempty"`
	EveryMS       int64     `json:"every_ms"`
	Until         int64     `json:"until"`
	NextCheck     int64     `json:"next_check,omitempty"`
	Checks        int       `json:"checks"`
	Reports       int       `json:"reports"`
	LastCheckAt   int64     `json:"last_check_at,omitempty"`
	LastReportAt  int64     `json:"last_report_at,omitempty"`
	LastOutcome   string    `json:"last_outcome,omitempty"`
	AgentURL      string    `json:"agent_url"`
}

// HelperDelta is one ephemeral A2A exchange or helper state change.
type HelperDelta struct {
	Handle    uint64    `json:"handle"`
	Contact   ContactID `json:"contact,omitempty"`
	Title     string    `json:"title,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	State     string    `json:"state,omitempty"`
	Event     string    `json:"event"`
	Method    string    `json:"method,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Text      string    `json:"text,omitempty"`
	Wire      string    `json:"wire,omitempty"`
	NextCheck int64     `json:"next_check,omitempty"`
	Checks    int       `json:"checks,omitempty"`
	Reports   int       `json:"reports,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
}
