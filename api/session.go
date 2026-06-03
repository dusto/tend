package api

// SessionStatus is an agent session's lifecycle state, as tracked by the daemon
// and reflected to attached clients.
type SessionStatus string

// SessionStatus values.
const (
	// StatusIdle: the session exists with no in-flight turn.
	StatusIdle SessionStatus = "idle"
	// StatusRunning: a turn is in flight.
	StatusRunning SessionStatus = "running"
	// StatusWaitingApproval: a turn is blocked on a pending approval.
	StatusWaitingApproval SessionStatus = "waiting_approval"
	// StatusWaitingClarification: a turn is blocked on a pending clarification.
	StatusWaitingClarification SessionStatus = "waiting_clarification"
	// StatusError: the session's last turn failed.
	StatusError SessionStatus = "error"
	// StatusEnded: the session is terminated (terminal).
	StatusEnded SessionStatus = "ended"
)

// PendingKind is the kind of interaction a waiting session is blocked on.
type PendingKind string

// PendingKind values.
const (
	PendingApproval      PendingKind = "approval"
	PendingClarification PendingKind = "clarification"
)
