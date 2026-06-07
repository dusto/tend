package events

import (
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func rawEvent(stream api.StreamID, seq uint64, typ string) api.Event {
	return api.Event{StreamID: stream, Kind: api.KindEvent, Seq: seq, CursorSeq: seq, Type: typ}
}

func TestDeduperRedelivery(t *testing.T) {
	d := NewDeduper()
	ev := rawEvent("session:s1", 1, "agent_message_chunk")

	if !d.Fresh(ev) {
		t.Fatal("first delivery should be fresh")
	}
	if d.Fresh(ev) {
		t.Error("redelivery of the same (stream, seq, kind) should be a duplicate")
	}
	// Same seq on a different stream is a different key.
	if !d.Fresh(rawEvent("session:s2", 1, "agent_message_chunk")) {
		t.Error("same seq on another stream should be fresh")
	}
}

func TestDeduperSummaryIdentity(t *testing.T) {
	d := NewDeduper()
	stream := api.StreamID("session:s1")

	// The client processed a raw event at seq 5.
	if !d.Fresh(rawEvent(stream, 5, "tool_call")) {
		t.Fatal("raw event should be fresh")
	}
	// A summary occupies from_seq 5 but with kind=summary: a distinct key, so it
	// is NOT dropped against the raw event at the same seq.
	summary := api.Event{
		StreamID: stream, Kind: api.KindSummary, Seq: 5, CursorSeq: 10,
		Summary: &api.SummaryInfo{FromSeq: 5, ToSeq: 10},
	}
	if !d.Fresh(summary) {
		t.Error("summary at from_seq must not be a dedup collision with the raw event there")
	}
	// The summary itself dedups on redelivery.
	if d.Fresh(summary) {
		t.Error("redelivered summary should be a duplicate")
	}
}

func TestDeduperForgetThrough(t *testing.T) {
	d := NewDeduper()
	stream := api.StreamID("session:s1")
	for seq := uint64(1); seq <= 3; seq++ {
		d.Fresh(rawEvent(stream, seq, "x"))
	}
	// Persisted cursor past seq 2: those raw keys can be forgotten.
	d.ForgetThrough(stream, 2)
	if !d.Fresh(rawEvent(stream, 1, "x")) {
		t.Error("forgotten key should be treated as fresh again")
	}
	if d.Fresh(rawEvent(stream, 3, "x")) {
		t.Error("key above the forget watermark should still dedup")
	}
}

// TestDeduperWithStoreRedelivery proves at-least-once + dedup end to end: the
// store replays a stream on each Read (as a reconnect would), and the Deduper
// drops the redelivered records.
func TestDeduperWithStoreRedelivery(t *testing.T) {
	log, err := OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := NewStore(log)

	stream := api.StreamID("session:s1")
	for range 3 {
		if _, err := store.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	d := NewDeduper()
	first, _, err := store.Read(stream, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	fresh := 0
	for _, ev := range first {
		if d.Fresh(ev) {
			fresh++
		}
	}
	if fresh != 3 {
		t.Fatalf("first read fresh = %d, want 3", fresh)
	}

	// A reconnect replays the same range; every record is a duplicate.
	second, _, _ := store.Read(stream, 0, 100)
	for _, ev := range second {
		if d.Fresh(ev) {
			t.Errorf("redelivered seq %d should be a duplicate", ev.Seq)
		}
	}
}
