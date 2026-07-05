package resource

import (
	"errors"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// fakeProc is a scripted process-stat source: successive reads pop the queue.
type fakeProc struct {
	ticks []uint64
	rss   int64
	err   error
	calls int
}

func (f *fakeProc) read(int) (uint64, int64, error) {
	if f.err != nil {
		return 0, 0, f.err
	}
	t := f.ticks[min(f.calls, len(f.ticks)-1)]
	f.calls++
	return t, f.rss, nil
}

func newSampler(read func(int) (uint64, int64, error), clock func() time.Time) *Sampler {
	return &Sampler{read: read, now: clock, prev: make(map[int]cpuSnapshot)}
}

func TestSamplerFirstSampleHasNoCPU(t *testing.T) {
	f := &fakeProc{ticks: []uint64{500}, rss: 4096}
	s := newSampler(f.read, func() time.Time { return time.Unix(100, 0) })

	u, ok := s.Sample(1)
	if !ok {
		t.Fatal("Sample not ok")
	}
	if u.CPUPercent != 0 {
		t.Errorf("first sample CPU = %v, want 0 (no interval yet)", u.CPUPercent)
	}
	if u.RSSBytes != 4096 {
		t.Errorf("RSS = %d, want 4096", u.RSSBytes)
	}
}

func TestSamplerComputesCPUPercentFromDelta(t *testing.T) {
	// 100 ticks over 1s at 100 ticks/sec = 1.0 CPU-second per wall second = 100%.
	f := &fakeProc{ticks: []uint64{1000, 1100}, rss: 8192}
	now := time.Unix(0, 0)
	s := newSampler(f.read, func() time.Time { return now })

	if _, ok := s.Sample(7); !ok {
		t.Fatal("first Sample not ok")
	}
	now = now.Add(time.Second)
	u, ok := s.Sample(7)
	if !ok {
		t.Fatal("second Sample not ok")
	}
	if u.CPUPercent != 100 {
		t.Errorf("CPU%% = %v, want 100", u.CPUPercent)
	}
	// Half the ticks over the same second would be 50%.
	f.ticks = []uint64{1100, 1150}
	f.calls = 0
	s2 := newSampler(f.read, func() time.Time { return now })
	_, _ = s2.Sample(7)
	now = now.Add(time.Second)
	if u2, _ := s2.Sample(7); u2.CPUPercent != 50 {
		t.Errorf("CPU%% = %v, want 50", u2.CPUPercent)
	}
}

func TestSamplerUnavailableProcess(t *testing.T) {
	f := &fakeProc{err: errors.New("no such process")}
	s := newSampler(f.read, time.Now)
	if _, ok := s.Sample(999); ok {
		t.Error("Sample of a gone process should not be ok")
	}
}

func TestSamplerRetainPrunesState(t *testing.T) {
	f := &fakeProc{ticks: []uint64{10}, rss: 1}
	s := newSampler(f.read, time.Now)
	_, _ = s.Sample(1)
	_, _ = s.Sample(2)

	s.Retain(map[int]struct{}{1: {}})
	s.mu.Lock()
	_, has1 := s.prev[1]
	_, has2 := s.prev[2]
	s.mu.Unlock()
	if !has1 || has2 {
		t.Errorf("after Retain{1}: has1=%v has2=%v, want true/false", has1, has2)
	}
}

func TestMonitorTickSamplesAndSinks(t *testing.T) {
	f := &fakeProc{ticks: []uint64{0}, rss: 2048}
	got := map[api.SessionID]*api.SessionResourceUsage{}
	m := &Monitor{
		sampler: newSampler(f.read, func() time.Time { return time.Unix(1, 0) }),
		pids:    func() map[api.SessionID]int { return map[api.SessionID]int{"s1": 11} },
		sink:    func(id api.SessionID, u *api.SessionResourceUsage) { got[id] = u },
	}
	m.tick()

	u, ok := got["s1"]
	if !ok || u == nil {
		t.Fatalf("sink got = %+v, want a sample for s1", got)
	}
	if u.RSSBytes != 2048 {
		t.Errorf("RSS = %d, want 2048", u.RSSBytes)
	}
}

func TestMonitorClearsDeadSession(t *testing.T) {
	// A session sampled one tick, then gone (no live pid) the next, must have its
	// stale usage cleared so session.list never reports a dead process.
	f := &fakeProc{ticks: []uint64{0}, rss: 4096}
	got := map[api.SessionID]*api.SessionResourceUsage{}
	live := map[api.SessionID]int{"s1": 11}
	m := &Monitor{
		sampler: newSampler(f.read, func() time.Time { return time.Unix(1, 0) }),
		pids:    func() map[api.SessionID]int { return live },
		sink:    func(id api.SessionID, u *api.SessionResourceUsage) { got[id] = u },
	}
	m.tick()
	if got["s1"] == nil {
		t.Fatal("s1 should have a sample after the first tick")
	}
	// The session's process is gone: it drops out of the live set.
	live = map[api.SessionID]int{}
	m.tick()
	if got["s1"] != nil {
		t.Errorf("dead session usage = %+v, want cleared to nil", got["s1"])
	}
}

func TestMonitorTickClearsUnavailable(t *testing.T) {
	f := &fakeProc{err: errors.New("gone")}
	got := map[api.SessionID]*api.SessionResourceUsage{"s1": {RSSBytes: 1}}
	m := &Monitor{
		sampler: newSampler(f.read, time.Now),
		pids:    func() map[api.SessionID]int { return map[api.SessionID]int{"s1": 11} },
		sink:    func(id api.SessionID, u *api.SessionResourceUsage) { got[id] = u },
	}
	m.tick()
	if got["s1"] != nil {
		t.Errorf("unavailable process should clear the sample, got %+v", got["s1"])
	}
}
