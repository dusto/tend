package api

// Params/result types for the plugin->daemon file methods.

// TextEdit replaces the bytes spanned by Range with NewText. The edits in a
// patch are expressed against the base snapshot and must be non-overlapping; see
// the patch package for application semantics and the {line, byte_col} position
// unit.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"new_text"`
}

// FileReadParams reads a repo file editor-aware. SessionID selects the session
// whose bound editor (if any) provides live buffer content.
type FileReadParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri"`
}

// FileReadResult returns the file content and the base revision it reflects, so
// a follow-up patch can cite the exact revision its conflict check will verify.
// Open is true when the content came from a live editor buffer (base is a
// changedtick); otherwise it came from disk (base is a content hash).
type FileReadResult struct {
	Content string   `json:"content"`
	Base    FileBase `json:"base"`
	Open    bool     `json:"open"`
}

// FilePatchParams applies an ordered set of non-overlapping text edits to a repo
// file. SessionID makes the edit task-bound; Base is the revision the edits were
// computed against (from file.read) and is re-verified before applying.
type FilePatchParams struct {
	SessionID SessionID  `json:"session_id"`
	URI       string     `json:"uri"`
	Edits     []TextEdit `json:"edits"`
	Base      FileBase   `json:"base"`
}

// FileWriteParams replaces a repo file's whole content. Base is the revision the
// new content was computed against and is re-verified before applying.
type FileWriteParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri"`
	Content   string    `json:"content"`
	Base      FileBase  `json:"base"`
}

// FileChange kinds: whether a change-set target applies edits or whole content.
const (
	FileChangePatch = "patch"
	FileChangeWrite = "write"
)

// FileChange is one target in a change set: a patch (Edits) or a whole-content
// write (Content), against Base. Kind selects which; an empty Kind is treated as
// a patch.
type FileChange struct {
	URI     string     `json:"uri"`
	Base    FileBase   `json:"base"`
	Kind    string     `json:"kind,omitempty"`
	Edits   []TextEdit `json:"edits,omitempty"`
	Content string     `json:"content,omitempty"`
}

// FileApplyChangeSetParams applies a multi-file change set as one approved unit.
type FileApplyChangeSetParams struct {
	SessionID SessionID    `json:"session_id"`
	Changes   []FileChange `json:"changes"`
}

// FileChangeOutcome reports what happened to one target of a change set.
type FileChangeOutcome struct {
	URI     string   `json:"uri"`
	Applied bool     `json:"applied"`
	Base    FileBase `json:"base,omitzero"` // new base when applied
	// RolledBack is true if the target was written then restored after a later
	// failure in the set.
	RolledBack bool `json:"rolled_back,omitempty"`
	// Error explains a non-applied (or un-rolled-back) target: a conflict, an
	// invalid edit, a write failure, or a failed rollback.
	Error string `json:"error,omitempty"`
}

// FileApplyChangeSetResult reports a change set's outcome. Applied is true only
// when every target applied. The daemon does not claim true atomicity across
// disk and editor buffers; on a mid-apply failure it reports exactly what
// applied, what did not, and what could not be rolled back.
type FileApplyChangeSetResult struct {
	ChangeSetID ChangeSetID         `json:"change_set_id"`
	Applied     bool                `json:"applied"`
	Reason      string              `json:"reason,omitempty"`
	Files       []FileChangeOutcome `json:"files"`
}

// FileMutationResult reports the outcome of a file.patch or file.write. Every
// mutation is a change set with a daemon-assigned id. Applied is false when the
// approval was denied (Reason explains); a stale base instead returns a conflict
// error. Base is the file's new base after a successful apply.
type FileMutationResult struct {
	ChangeSetID ChangeSetID `json:"change_set_id"`
	Applied     bool        `json:"applied"`
	Reason      string      `json:"reason,omitempty"`
	Base        FileBase    `json:"base,omitzero"`
}
