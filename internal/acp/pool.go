package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dusto/tend/api"
)

// ErrPoolClosed is returned by Acquire after the pool has been closed.
var ErrPoolClosed = errors.New("acp: pool closed")

// ErrProcessGone is returned by AcquireOn when the target process is no longer
// in the pool (it crashed or was closed).
var ErrProcessGone = errors.New("acp: process no longer in pool")

// Process is the minimum a pooled ACP process must expose: a Done channel that
// closes when it exits, and Close to terminate it. *Client satisfies it.
type Process interface {
	Done() <-chan struct{}
	Close() error
}

// SpawnFunc creates and initializes a new process for key. The pool calls it
// when it needs another process for a {workspace, provider}.
type SpawnFunc func(ctx context.Context, key Key) (Process, error)

// Emitter publishes events. *events.Store satisfies it.
type Emitter interface {
	Publish(api.Event) (api.Event, error)
}

// Key identifies a process pool: one per {workspace, provider}.
type Key struct {
	Workspace api.WorkspaceID
	Provider  api.ProviderID
}

// Options configures a Pool.
type Options struct {
	// Max is the most processes the pool keeps per key. Values < 1 mean 1.
	Max int
	// IdleTTL is how long an idle process is kept before eviction. 0 disables
	// time-based eviction.
	IdleTTL time.Duration
	// Now returns the current time; nil uses time.Now. Tests inject this to
	// drive idle eviction deterministically.
	Now func() time.Time
}

// Pool keeps long-lived ACP processes per {workspace, provider} and schedules
// in-flight turns onto them. Busy means a process is serving a turn; a turn
// holds a Lease. Acquire reuses an idle process, spawns another up to Max, or
// blocks until one frees. A crashed process is removed, its in-flight turn is
// failed, and stop/error events are emitted. A Pool is safe for concurrent use.
type Pool struct {
	spawn SpawnFunc
	emit  Emitter
	max   int
	ttl   time.Duration
	now   func() time.Time

	mu     sync.Mutex
	pools  map[Key]*keyPool
	closed bool

	wg       sync.WaitGroup // process crash watchers
	janitorD chan struct{}  // closed to stop the eviction janitor
}

type keyPool struct {
	procs  []*procEntry
	notify chan struct{} // closed to broadcast a state change, then replaced
}

func (kp *keyPool) broadcast() {
	close(kp.notify)
	kp.notify = make(chan struct{})
}

type procEntry struct {
	proc      Process
	busy      bool
	removed   bool
	retiring  bool // intentionally closed (evict/Close): no crash events
	idleSince time.Time
	sessionID api.SessionID // the current turn's session, for crash attribution
	sessions  int           // sessions hosted on this process; >0 blocks eviction
	dead      chan struct{} // closed when the process leaves the pool
}

// NewPool returns a Pool that creates processes with spawn and emits crash
// events through emit (which may be nil to skip emission). It starts an idle
// eviction janitor when opts.IdleTTL > 0.
func NewPool(spawn SpawnFunc, emit Emitter, opts Options) *Pool {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	max := opts.Max
	if max < 1 {
		max = 1
	}
	p := &Pool{
		spawn:    spawn,
		emit:     emit,
		max:      max,
		ttl:      opts.IdleTTL,
		now:      now,
		pools:    make(map[Key]*keyPool),
		janitorD: make(chan struct{}),
	}
	if p.ttl > 0 {
		go p.janitor()
	}
	return p
}

// Lease is a held, busy process for one in-flight turn. Release frees it back to
// the pool; Done closes if the process leaves the pool (e.g. a crash) before the
// turn finishes.
type Lease struct {
	pool *Pool
	key  Key
	e    *procEntry
	once sync.Once
}

// Process returns the leased process.
func (l *Lease) Process() Process { return l.e.proc }

// Done closes if the underlying process leaves the pool before Release.
func (l *Lease) Done() <-chan struct{} { return l.e.dead }

// Release returns the process to the pool. It is idempotent.
func (l *Lease) Release() { l.once.Do(func() { l.pool.release(l.key, l.e) }) }

// Acquire obtains a process for a turn on behalf of sessionID, reusing an idle
// process for key, spawning another up to Max, or blocking until one frees or
// ctx is done.
func (p *Pool) Acquire(ctx context.Context, key Key, sessionID api.SessionID) (*Lease, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}
		kp := p.keyPoolLocked(key)

		if e := idleEntry(kp); e != nil {
			e.busy = true
			e.sessionID = sessionID
			lease := &Lease{pool: p, key: key, e: e}
			p.mu.Unlock()
			return lease, nil
		}

		if len(kp.procs) < p.max {
			e := &procEntry{busy: true, sessionID: sessionID, dead: make(chan struct{})}
			kp.procs = append(kp.procs, e) // reserve the slot before spawning
			p.mu.Unlock()

			proc, err := p.spawn(ctx, key)
			p.mu.Lock()
			if err != nil {
				p.removeLocked(kp, e)
				kp.broadcast()
				p.mu.Unlock()
				return nil, err
			}
			if p.closed {
				// Close ran during the spawn and could not see this process
				// (e.proc was still nil), so terminate it here rather than hand
				// out a lease from a closed pool.
				p.removeLocked(kp, e)
				p.mu.Unlock()
				_ = proc.Close()
				return nil, ErrPoolClosed
			}
			e.proc = proc
			p.watch(key, e)
			lease := &Lease{pool: p, key: key, e: e}
			p.mu.Unlock()
			return lease, nil
		}

		// At capacity with all processes busy: wait for a state change.
		wait := kp.notify
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// AcquireOn obtains a turn on a specific process: it waits until proc has no
// in-flight turn, marks it busy, and returns a Lease. It is how a pinned session
// runs a turn on its own process while still serializing turns through the pool
// (so eviction never reaps a process mid-turn). It returns ErrProcessGone if
// proc has left the pool.
func (p *Pool) AcquireOn(ctx context.Context, key Key, proc Process, sessionID api.SessionID) (*Lease, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}
		kp := p.pools[key]
		e := findEntry(kp, proc)
		if e == nil {
			p.mu.Unlock()
			return nil, ErrProcessGone
		}
		if !e.busy {
			e.busy = true
			e.sessionID = sessionID
			lease := &Lease{pool: p, key: key, e: e}
			p.mu.Unlock()
			return lease, nil
		}
		wait := kp.notify
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// AddSession records that proc now hosts another session, so idle eviction will
// not reap it while sessions remain. RemoveSession is the inverse.
func (p *Pool) AddSession(key Key, proc Process) { p.adjustSessions(key, proc, +1) }

// RemoveSession drops one hosted-session count from proc.
func (p *Pool) RemoveSession(key Key, proc Process) { p.adjustSessions(key, proc, -1) }

func (p *Pool) adjustSessions(key Key, proc Process, delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := findEntry(p.pools[key], proc); e != nil {
		e.sessions += delta
		if e.sessions < 0 {
			e.sessions = 0
		}
	}
}

// Close terminates all processes and stops the pool. Subsequent Acquire calls
// return ErrPoolClosed. It waits for crash watchers to finish.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.janitorD)
	var procs []Process
	for _, kp := range p.pools {
		for _, e := range kp.procs {
			e.retiring = true
			if e.proc != nil {
				procs = append(procs, e.proc)
			}
		}
		kp.broadcast() // wake any waiters so they observe closed
	}
	p.mu.Unlock()

	for _, proc := range procs {
		_ = proc.Close()
	}
	p.wg.Wait()
	return nil
}

func (p *Pool) release(key Key, e *procEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.removed {
		return
	}
	e.busy = false
	e.sessionID = ""
	e.idleSince = p.now()
	if kp := p.pools[key]; kp != nil {
		kp.broadcast()
	}
}

// watch removes the entry and fails its turn when the process exits, emitting
// stop/error events unless the exit was an intentional retirement.
func (p *Pool) watch(key Key, e *procEntry) {
	proc := e.proc
	p.wg.Go(func() {
		<-proc.Done()

		p.mu.Lock()
		kp := p.pools[key]
		if kp != nil {
			p.removeLocked(kp, e)
			kp.broadcast() // a slot freed; wake waiters to spawn a replacement
		}
		retiring, busy, sessionID := e.retiring, e.busy, e.sessionID
		p.mu.Unlock()

		close(e.dead) // fail any in-flight lease
		if retiring {
			return
		}
		p.emitStopped(key.Provider, "provider process exited")
		if busy && sessionID != "" {
			p.emitAgentError(sessionID, "provider process exited mid-turn")
		}
	})
}

// evictIdle closes idle processes whose idle time has reached the TTL. It is the
// janitor's unit of work and is called directly by tests.
func (p *Pool) evictIdle() {
	if p.ttl <= 0 {
		return
	}
	p.mu.Lock()
	now := p.now()
	var toClose []Process
	for _, kp := range p.pools {
		for _, e := range kp.procs {
			// Never evict a process that is busy, already retiring, or still
			// hosting sessions (evicting it would fail those sessions).
			if !e.busy && !e.removed && !e.retiring && e.sessions == 0 && e.proc != nil && now.Sub(e.idleSince) >= p.ttl {
				e.retiring = true
				toClose = append(toClose, e.proc)
			}
		}
	}
	p.mu.Unlock()
	for _, proc := range toClose {
		_ = proc.Close() // the watcher removes the entry silently (retiring)
	}
}

func (p *Pool) janitor() {
	t := time.NewTicker(p.ttl)
	defer t.Stop()
	for {
		select {
		case <-p.janitorD:
			return
		case <-t.C:
			p.evictIdle()
		}
	}
}

func (p *Pool) keyPoolLocked(key Key) *keyPool {
	kp := p.pools[key]
	if kp == nil {
		kp = &keyPool{notify: make(chan struct{})}
		p.pools[key] = kp
	}
	return kp
}

func (p *Pool) removeLocked(kp *keyPool, e *procEntry) {
	if e.removed {
		return
	}
	e.removed = true
	for i, x := range kp.procs {
		if x == e {
			kp.procs = append(kp.procs[:i], kp.procs[i+1:]...)
			break
		}
	}
}

func findEntry(kp *keyPool, proc Process) *procEntry {
	if kp == nil {
		return nil
	}
	for _, e := range kp.procs {
		if e.proc == proc {
			return e
		}
	}
	return nil
}

func idleEntry(kp *keyPool) *procEntry {
	for _, e := range kp.procs {
		// A retiring entry is being intentionally closed; never lease it (its
		// exit is silent, so a turn assigned to it would fail without events).
		if !e.busy && !e.retiring && e.proc != nil {
			return e
		}
	}
	return nil
}

func (p *Pool) emitStopped(provider api.ProviderID, reason string) {
	if p.emit == nil {
		return
	}
	payload, _ := json.Marshal(api.ProviderStopped{ProviderID: provider, Reason: reason})
	_, _ = p.emit.Publish(api.Event{
		StreamID: api.StreamID("provider:" + provider),
		Scope:    api.ScopeProvider,
		Type:     "provider_stopped",
		Payload:  payload,
	})
}

func (p *Pool) emitAgentError(session api.SessionID, msg string) {
	if p.emit == nil {
		return
	}
	payload, _ := json.Marshal(api.AgentError{SessionID: session, Message: msg})
	_, _ = p.emit.Publish(api.Event{
		StreamID: api.StreamID("session:" + session),
		Scope:    api.ScopeSession,
		Type:     "agent_error",
		Payload:  payload,
	})
}
