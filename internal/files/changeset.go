package files

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/diff"
	"github.com/dusto/tend/internal/patch"
)

// preparedChange is a change-set target that passed preflight: its current state,
// the proposed new content, and the diff for the approval.
type preparedChange struct {
	idx        int // index into the request's Changes, for outcome alignment
	uri, path  string
	base       api.FileBase
	open       bool
	tick       *int64
	before     []byte
	newContent []byte
	diff       string
}

// ApplyChangeSet applies a multi-file change set as one approved unit. It
// preflights every target's base (aborting the whole set with nothing applied on
// any mismatch), requests one approval for the set, then applies disk writes
// first (temp+rename, keeping backups) and editor buffers last. On a mid-apply
// failure it rolls back the disk writes it can and reports an explicit
// partial-failure result rather than claiming atomicity.
func (s *Service) ApplyChangeSet(ctx context.Context, p api.FileApplyChangeSetParams) (api.FileApplyChangeSetResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.FileApplyChangeSetResult{}, ErrNoSession
	}
	if !sess.HasTask() {
		return api.FileApplyChangeSetResult{}, ErrNoTask
	}
	csid := s.newID()
	res := api.FileApplyChangeSetResult{ChangeSetID: csid, Files: make([]api.FileChangeOutcome, len(p.Changes))}
	for i, ch := range p.Changes {
		res.Files[i].URI = ch.URI
	}

	// Preflight: resolve, verify base, and compute new content for every target.
	// Any failure aborts the whole set before approval — nothing is applied.
	var preps []preparedChange
	aborted := false
	for i, ch := range p.Changes {
		path, err := resolvePath(ch.URI, sess.WorktreeRoot)
		if err != nil {
			res.Files[i].Error = err.Error()
			aborted = true
			continue
		}
		st, err := s.readCurrent(ctx, p.SessionID, ch.URI, path)
		if err != nil {
			res.Files[i].Error = err.Error()
			aborted = true
			continue
		}
		if err := verifyBase(ch.Base, st); err != nil {
			res.Files[i].Error = err.Error()
			aborted = true
			continue
		}
		newContent, err := newContentFor(ch, st.content)
		if err != nil {
			res.Files[i].Error = err.Error()
			aborted = true
			continue
		}
		preps = append(preps, preparedChange{
			idx: i, uri: ch.URI, path: path, base: ch.Base,
			open: st.open, tick: st.tick, before: st.content, newContent: newContent,
			diff: diff.Unified(string(st.content), string(newContent)),
		})
	}
	if aborted {
		res.Reason = "preflight failed; nothing applied"
		return res, nil
	}

	// Snapshots are captured at proposal time, so the set is reviewable via
	// file.diff even while pending or after a denial.
	snaps := make([]changeSetFile, len(preps))
	for k, pr := range preps {
		snaps[k] = changeSetFile{
			uri: pr.uri, before: string(pr.before), after: string(pr.newContent), diff: pr.diff,
		}
	}
	s.snapshots.record(csid, p.SessionID, snaps)

	// One approval for the whole set.
	targets := make([]api.FileEditTarget, len(preps))
	for k, pr := range preps {
		targets[k] = api.FileEditTarget{URI: pr.uri, Base: pr.base, Diff: pr.diff}
	}
	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind:     api.ApprovalFileEdit,
		FileEdit: &api.FileEditApproval{ChangeSetID: csid, Targets: targets},
	})
	outcome, err := s.approver.Request(ctx, sess, api.ApprovalFileEdit, detail)
	if err != nil {
		return api.FileApplyChangeSetResult{}, err
	}
	if !outcome.Approved {
		res.Reason = outcome.Reason
		if res.Reason == "" {
			res.Reason = "denied"
		}
		return res, nil
	}

	res = s.applyPrepared(ctx, p.SessionID, res, preps)
	appliedByURI := make(map[string]bool, len(res.Files))
	for _, f := range res.Files {
		appliedByURI[f.URI] = f.Applied
	}
	s.snapshots.setApplied(csid, res.Applied, appliedByURI)
	return res, nil
}

// applyPrepared performs the apply phase: disk writes first (with backups for
// rollback), editor buffers last.
func (s *Service) applyPrepared(ctx context.Context, sessionID api.SessionID, res api.FileApplyChangeSetResult, preps []preparedChange) api.FileApplyChangeSetResult {
	type backup struct {
		idx  int
		path string
		data []byte
	}
	var disk []backup // applied disk writes, for rollback

	rollback := func() {
		for _, b := range disk {
			if err := writeFileAtomic(b.path, b.data); err != nil {
				res.Files[b.idx].Error = "applied, but rollback failed: " + err.Error()
				res.Files[b.idx].Applied = true // still on disk
			} else {
				res.Files[b.idx].Applied = false
				res.Files[b.idx].RolledBack = true
				res.Files[b.idx].Base = api.FileBase{}
			}
		}
	}

	// Disk writes (closed files) first.
	for _, pr := range preps {
		if pr.open {
			continue
		}
		// Re-verify the base right before writing, so a change between approval and
		// apply is caught rather than overwritten.
		st, err := s.readCurrent(ctx, sessionID, pr.uri, pr.path)
		if err != nil || verifyBase(pr.base, st) != nil {
			res.Files[pr.idx].Error = "conflict: base changed before apply"
			rollback()
			res.Reason = "aborted mid-apply; disk writes rolled back"
			return res
		}
		if err := writeFileAtomic(pr.path, pr.newContent); err != nil {
			res.Files[pr.idx].Error = err.Error()
			rollback()
			res.Reason = "aborted mid-apply; disk writes rolled back"
			return res
		}
		disk = append(disk, backup{idx: pr.idx, path: pr.path, data: st.content})
		res.Files[pr.idx].Applied = true
		res.Files[pr.idx].Base = api.FileBase{ContentHash: patch.ContentHash(pr.newContent)}
	}

	// Editor buffers last: disk is already committed, so an editor failure is
	// isolated and reported rather than triggering a disk rollback.
	for _, pr := range preps {
		if !pr.open {
			continue
		}
		st, err := s.readCurrent(ctx, sessionID, pr.uri, pr.path)
		if err != nil || verifyBase(pr.base, st) != nil {
			res.Files[pr.idx].Error = "conflict: buffer changed before apply"
			res.Reason = "partial: disk applied, an editor buffer was not"
			return res
		}
		wr, err := s.editors.WriteBuffer(ctx, sessionID, api.EditorWriteBufferParams{
			URI: pr.uri, Content: string(pr.newContent), Base: api.FileBase{ChangedTick: pr.tick},
		})
		if err != nil {
			res.Files[pr.idx].Error = err.Error()
			res.Reason = "partial: disk applied, an editor buffer was not"
			return res
		}
		res.Files[pr.idx].Applied = true
		res.Files[pr.idx].Base = wr.Base
	}

	res.Applied = true
	return res
}

// newContentFor computes a change-set target's proposed content from the current
// content: applying its edits (patch) or replacing it (write).
func newContentFor(ch api.FileChange, cur []byte) ([]byte, error) {
	switch ch.Kind {
	case api.FileChangeWrite:
		return []byte(ch.Content), nil
	case api.FileChangePatch, "":
		return patch.Apply(cur, ch.Edits)
	default:
		return nil, fmt.Errorf("files: unknown change kind %q", ch.Kind)
	}
}
