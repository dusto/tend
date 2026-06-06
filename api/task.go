package api

import "time"

// Task is the wire view of a provider task.
type Task struct {
	Ref         TaskRef       `json:"ref"`
	Title       string        `json:"title"`
	Status      string        `json:"status"`
	Description string        `json:"description,omitempty"`
	Assignee    string        `json:"assignee,omitempty"`
	Labels      []string      `json:"labels,omitempty"`
	Comments    []TaskComment `json:"comments,omitempty"`
}

// TaskComment is a note attached to a task.
type TaskComment struct {
	Author string    `json:"author,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// TaskCreateParams creates a task in a workspace's provider.
type TaskCreateParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Labels      []string    `json:"labels,omitempty"`
}

// TaskShowParams fetches one task.
type TaskShowParams struct {
	Ref TaskRef `json:"ref"`
}

// TaskListParams lists a workspace's tasks, optionally filtered by status.
type TaskListParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	Status      string      `json:"status,omitempty"`
}

// TaskListResult is the set of matching tasks.
type TaskListResult struct {
	Tasks []Task `json:"tasks"`
}

// TaskClaimParams assigns a task and marks it in progress.
type TaskClaimParams struct {
	Ref      TaskRef `json:"ref"`
	Assignee string  `json:"assignee"`
}

// TaskCommentParams appends a comment to a task.
type TaskCommentParams struct {
	Ref    TaskRef `json:"ref"`
	Text   string  `json:"text"`
	Author string  `json:"author,omitempty"`
}

// TaskCloseParams closes a task.
type TaskCloseParams struct {
	Ref TaskRef `json:"ref"`
}

// TaskChange is the payload of a task_* event: the task that changed. Clients
// fetch full state with task.show.
type TaskChange struct {
	Ref TaskRef `json:"ref"`
}
