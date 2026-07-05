package resource

import (
	"context"
	"time"

	"github.com/dusto/tend/api"
)

// Monitor periodically samples the resource usage of a set of sessions' agent
// processes and reports each sample to a sink. It runs off the request hot path:
// a client reads the latest sample from session state, never triggering a sample
// itself.
type Monitor struct {
	sampler  *Sampler
	pids     func() map[api.SessionID]int
	sink     func(api.SessionID, *api.SessionResourceUsage)
	interval time.Duration

	// reported tracks sessions with a stored sample, so one whose process is gone
	// gets its stale usage cleared once, rather than lingering forever.
	reported map[api.SessionID]struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

// NewMonitor returns a Monitor that every interval samples the pids returned by
// pids and reports each to sink. pids returns the live session-to-agent-pid map;
// sink stores a session's latest sample (nil clears it when the process cannot be
// sampled).
func NewMonitor(interval time.Duration, pids func() map[api.SessionID]int, sink func(api.SessionID, *api.SessionResourceUsage)) *Monitor {
	return &Monitor{sampler: NewSampler(), pids: pids, sink: sink, interval: interval, reported: make(map[api.SessionID]struct{})}
}

// Start launches the sampling loop until Close. It is a no-op if the interval is
// not positive.
func (m *Monitor) Start() {
	if m.interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick()
			}
		}
	}()
}

// Close stops the sampling loop and waits for it to exit.
func (m *Monitor) Close() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
}

// tick samples every live session's agent process once and reports each result.
// A session that is no longer live (its process exited) has any stored sample
// cleared, so session.list never reports stale CPU/RSS for a dead process. It
// then drops cached CPU state for pids that are no longer live.
func (m *Monitor) tick() {
	if m.reported == nil {
		m.reported = make(map[api.SessionID]struct{})
	}
	live := m.pids()
	keep := make(map[int]struct{}, len(live))
	seen := make(map[api.SessionID]struct{}, len(live))
	for id, pid := range live {
		keep[pid] = struct{}{}
		seen[id] = struct{}{}
		if u, ok := m.sampler.Sample(pid); ok {
			m.sink(id, &api.SessionResourceUsage{
				CPUPercent: u.CPUPercent,
				RSSBytes:   u.RSSBytes,
				SampledAt:  u.SampledAt,
			})
			m.reported[id] = struct{}{}
		} else {
			m.sink(id, nil)
			delete(m.reported, id)
		}
	}
	// Clear sessions that had a sample but are no longer live (process gone).
	for id := range m.reported {
		if _, ok := seen[id]; !ok {
			m.sink(id, nil)
			delete(m.reported, id)
		}
	}
	m.sampler.Retain(keep)
}
