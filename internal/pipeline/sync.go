package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/cmerk2021/slskify/internal/slskd"
)

// poller periodically fetches slskd's transfer list while anyone is
// watching, and wakes subscribers after each fetch. One shared poll serves
// every in-flight download.
type poller struct {
	sl       SlskdClient
	interval time.Duration
	onErr    func(string)

	mu        sync.Mutex
	subs      map[chan struct{}]bool
	transfers []slskd.Transfer
	wake      chan struct{}
	failures  int
}

func newPoller(sl SlskdClient, interval time.Duration, onErr func(string)) *poller {
	return &poller{sl: sl, interval: interval, onErr: onErr, subs: map[chan struct{}]bool{}, wake: make(chan struct{}, 1)}
}

func (p *poller) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	p.mu.Lock()
	p.subs[ch] = true
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return ch
}

func (p *poller) unsubscribe(ch chan struct{}) {
	p.mu.Lock()
	delete(p.subs, ch)
	p.mu.Unlock()
}

func (p *poller) run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
		p.mu.Lock()
		n := len(p.subs)
		p.mu.Unlock()
		if n == 0 {
			continue
		}
		ts, err := p.sl.Downloads(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.failures++
			// Report once per streak, not every poll.
			if p.failures == 3 && p.onErr != nil {
				p.onErr("cannot read slskd transfers: " + err.Error())
			}
			continue
		}
		p.failures = 0
		p.mu.Lock()
		p.transfers = ts
		for ch := range p.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		p.mu.Unlock()
	}
}

func (p *poller) find(id, user, path string) *slskd.Transfer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := findTransfer(p.transfers, id, user, path); t != nil {
		c := *t
		return &c
	}
	return nil
}

// workQueue is an unbounded FIFO of track IDs.
type workQueue struct {
	mu    sync.Mutex
	items []int64
	wake  chan struct{}
}

func newWorkQueue() *workQueue { return &workQueue{wake: make(chan struct{}, 1)} }

func (q *workQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Push appends id.
func (q *workQueue) Push(id int64) {
	q.mu.Lock()
	q.items = append(q.items, id)
	q.mu.Unlock()
	q.signal()
}

// PushFront prepends id (user-initiated work goes first).
func (q *workQueue) PushFront(id int64) {
	q.mu.Lock()
	q.items = append([]int64{id}, q.items...)
	q.mu.Unlock()
	q.signal()
}

// Pop blocks until an item is available or ctx is done.
func (q *workQueue) Pop(ctx context.Context) (int64, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			id := q.items[0]
			q.items = q.items[1:]
			more := len(q.items) > 0
			q.mu.Unlock()
			if more {
				q.signal()
			}
			return id, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, false
		case <-q.wake:
		}
	}
}

// Len returns the queue length.
func (q *workQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// gate blocks dispatching while closed (paused).
type gate struct {
	mu     sync.Mutex
	closed bool
	ch     chan struct{} // closed when the gate is open
}

func newGate() *gate {
	ch := make(chan struct{})
	close(ch)
	return &gate{ch: ch}
}

func (g *gate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		g.ch = make(chan struct{})
	}
}

func (g *gate) Open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		g.closed = false
		close(g.ch)
	}
}

func (g *gate) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

func (g *gate) Wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rateLimiter spaces search starts by at least interval, globally.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (l *rateLimiter) Wait(ctx context.Context) error {
	if l.interval <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	start := l.next
	if start.Before(now) {
		start = now
	}
	l.next = start.Add(l.interval)
	l.mu.Unlock()
	if d := time.Until(start); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
