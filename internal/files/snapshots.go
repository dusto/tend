package files

import (
	"errors"
	"sync"

	"github.com/dusto/tend/api"
)

// ErrUnknownChangeSet reports a change-set id with no retained snapshots:
// never recorded, or evicted from the retention window.
var ErrUnknownChangeSet = errors.New("files: unknown change set")

// changeSetFile is one target's captured snapshots within a change set.
type changeSetFile struct {
	uri, before, after, diff string
	applied                  bool
}

// changeSetRecord is the retained review view of one change set: the snapshots
// captured at proposal time plus the apply outcome.
type changeSetRecord struct {
	sessionID api.SessionID
	applied   bool
	files     []changeSetFile
}

// snapshotStore retains the before/after snapshots of recent change sets so
// file.diff (and the editor diff view fed from it) always has a concrete
// target: it diffs the named proposal or applied set, never an undefined
// "current state". Snapshots are recorded at proposal time — a denied or
// pending set is reviewable exactly as proposed — and bounded per session: the
// oldest set falls out once a session exceeds the retention cap.
type snapshotStore struct {
	mu      sync.Mutex
	retain  int
	records map[api.ChangeSetID]*changeSetRecord
	order   map[api.SessionID][]api.ChangeSetID
}

// newSnapshotStore returns a store keeping at most retain change sets per
// session.
func newSnapshotStore(retain int) *snapshotStore {
	return &snapshotStore{
		retain:  retain,
		records: make(map[api.ChangeSetID]*changeSetRecord),
		order:   make(map[api.SessionID][]api.ChangeSetID),
	}
}

// record captures a proposed change set's snapshots, evicting the session's
// oldest set beyond the retention cap.
func (st *snapshotStore) record(csid api.ChangeSetID, sessionID api.SessionID, files []changeSetFile) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.records[csid] = &changeSetRecord{sessionID: sessionID, files: files}
	ids := st.order[sessionID]
	ids = append(ids, csid)
	for len(ids) > st.retain {
		delete(st.records, ids[0])
		ids = ids[1:]
	}
	st.order[sessionID] = ids
}

// setApplied updates a recorded set's apply outcome: the set-level flag and
// which targets (by uri) actually applied. Unknown ids are ignored — an
// evicted record cannot be updated.
func (st *snapshotStore) setApplied(csid api.ChangeSetID, applied bool, appliedByURI map[string]bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.records[csid]
	if !ok {
		return
	}
	rec.applied = applied
	for i := range rec.files {
		rec.files[i].applied = appliedByURI[rec.files[i].uri]
	}
}

// get returns the review view of a recorded change set.
func (st *snapshotStore) get(csid api.ChangeSetID) (api.FileDiffResult, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.records[csid]
	if !ok {
		return api.FileDiffResult{}, false
	}
	res := api.FileDiffResult{
		ChangeSetID: csid,
		SessionID:   rec.sessionID,
		Applied:     rec.applied,
		Files:       make([]api.FileDiffEntry, len(rec.files)),
	}
	for i, f := range rec.files {
		res.Files[i] = api.FileDiffEntry{
			URI:     f.uri,
			Before:  f.before,
			After:   f.after,
			Diff:    f.diff,
			Applied: f.applied,
		}
	}
	return res, true
}
