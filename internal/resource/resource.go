// Package resource samples per-process OS resource usage (CPU and resident
// memory) for the daemon's session accounting. Sampling is best-effort and
// platform-specific: it reads Linux /proc and degrades to "unavailable" where
// unsupported, so callers surface an absent sample rather than an error.
package resource

import (
	"sync"
	"time"
)

// Usage is a point-in-time resource sample for one process.
type Usage struct {
	// CPUPercent is CPU used over the interval since the previous sample of the
	// same pid, as a percentage of one core. It is 0 on a pid's first sample.
	CPUPercent float64
	// RSSBytes is the resident set size in bytes.
	RSSBytes int64
	// SampledAt is when the sample was taken.
	SampledAt time.Time
}

// Sampler computes per-process CPU% and RSS. CPU% is a rate, so it keeps the
// previous CPU-time reading per pid and rates the delta over wall time between
// samples; the first sample of a pid reports RSS with CPUPercent 0. A Sampler is
// safe for concurrent use.
type Sampler struct {
	// read returns cumulative CPU time (in clock ticks) and RSS bytes for a pid.
	// It is a field so tests can drive the CPU-rate math without a real process.
	read func(pid int) (cpuTicks uint64, rssBytes int64, err error)
	now  func() time.Time

	mu   sync.Mutex
	prev map[int]cpuSnapshot
}

type cpuSnapshot struct {
	ticks uint64
	at    time.Time
}

// NewSampler returns a Sampler reading the current platform's process stats.
func NewSampler() *Sampler {
	return &Sampler{read: readProc, now: time.Now, prev: make(map[int]cpuSnapshot)}
}

// Sample reads pid's usage, computing CPU% from the delta since the previous
// Sample of the same pid. ok is false when the process cannot be read (it exited,
// or the platform is unsupported); in that case any cached CPU state for pid is
// dropped.
func (s *Sampler) Sample(pid int) (Usage, bool) {
	ticks, rss, err := s.read(pid)
	if err != nil {
		s.forget(pid)
		return Usage{}, false
	}
	now := s.now()

	s.mu.Lock()
	prev, had := s.prev[pid]
	s.prev[pid] = cpuSnapshot{ticks: ticks, at: now}
	s.mu.Unlock()

	u := Usage{RSSBytes: rss, SampledAt: now}
	if had {
		if dt := now.Sub(prev.at).Seconds(); dt > 0 && ticks >= prev.ticks {
			cpuSecs := float64(ticks-prev.ticks) / clockTicksPerSec
			u.CPUPercent = cpuSecs / dt * 100
		}
	}
	return u, true
}

// Retain drops cached CPU state for any pid not in keep, so the sampler does not
// accumulate state for processes it no longer samples (ended sessions).
func (s *Sampler) Retain(keep map[int]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid := range s.prev {
		if _, ok := keep[pid]; !ok {
			delete(s.prev, pid)
		}
	}
}

func (s *Sampler) forget(pid int) {
	s.mu.Lock()
	delete(s.prev, pid)
	s.mu.Unlock()
}
