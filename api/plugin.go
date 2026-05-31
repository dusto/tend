package api

// Params/result types for plugin->daemon methods.

// WorkspaceOpenParams opens or resolves a workspace from a directory.
type WorkspaceOpenParams struct {
	// Dir is any path inside the target repo/worktree; the daemon resolves the
	// containing git repo to derive WorkspaceID + worktree, or opens an
	// ephemeral read-only workspace if there is no git repo.
	Dir string `json:"dir"`
}

// WorkspaceCurrentParams has no fields; it returns the active workspace.
type WorkspaceCurrentParams struct{}

// EventsSubscribeParams subscribes to one logical stream from a cursor.
type EventsSubscribeParams struct {
	StreamID StreamID `json:"stream_id"`
	// LastSeq resumes delivery after this in-stream seq (0 = from the start of
	// the retained log for the stream).
	LastSeq uint64 `json:"last_seq"`
}

// EventsSubscribeResult is returned on a successful subscribe.
type EventsSubscribeResult struct {
	// Tail is the stream's current high-water seq at subscribe time.
	Tail uint64 `json:"tail"`
}

// EventsUnsubscribeParams stops delivery for a stream.
type EventsUnsubscribeParams struct {
	StreamID StreamID `json:"stream_id"`
}
