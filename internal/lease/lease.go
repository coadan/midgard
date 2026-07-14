package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"midgard/internal/state"
)

const (
	DefaultTTL           = 30 * time.Second
	DefaultRenewInterval = 10 * time.Second
)

type Options struct {
	OwnerID       string
	TTL           time.Duration
	RenewInterval time.Duration
	Now           func() time.Time
}

type Scope struct {
	Context context.Context
	guard   *Guard
	owned   bool
}

type Guard struct {
	db        *state.DB
	options   Options
	cancel    context.CancelCauseFunc
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error

	mu       sync.RWMutex
	lease    state.ExecutionLease
	renewErr error
}

type contextKey struct{}

type contextGuards struct {
	guard  *Guard
	parent *contextGuards
}

func Ensure(ctx context.Context, db *state.DB, resourceType, resourceID string, opts Options) (*Scope, error) {
	if existing := find(ctx, resourceType, resourceID); existing != nil {
		if err := existing.Check(ctx); err != nil {
			return nil, err
		}
		return &Scope{Context: ctx, guard: existing}, nil
	}
	guard, guardedCtx, err := Acquire(ctx, db, resourceType, resourceID, opts)
	if err != nil {
		return nil, err
	}
	return &Scope{Context: guardedCtx, guard: guard, owned: true}, nil
}

func Acquire(ctx context.Context, db *state.DB, resourceType, resourceID string, opts Options) (*Guard, context.Context, error) {
	opts = normalizeOptions(opts)
	lease, err := db.AcquireExecutionLease(ctx, resourceType, resourceID, opts.OwnerID, opts.Now(), opts.TTL)
	if err != nil {
		return nil, ctx, err
	}
	guardCtx, cancel := context.WithCancelCause(ctx)
	guard := &Guard{
		db: db, options: opts, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}), lease: lease,
	}
	guardCtx = state.ContextWithExecutionFence(guardCtx, lease)
	parent, _ := ctx.Value(contextKey{}).(*contextGuards)
	guardCtx = context.WithValue(guardCtx, contextKey{}, &contextGuards{guard: guard, parent: parent})
	go guard.renew(guardCtx)
	return guard, guardCtx, nil
}

func (s *Scope) Close() error {
	if s == nil || !s.owned {
		return nil
	}
	return s.guard.Close()
}

func (s *Scope) Lease() state.ExecutionLease {
	if s == nil || s.guard == nil {
		return state.ExecutionLease{}
	}
	return s.guard.Lease()
}

func Check(ctx context.Context) error {
	guards, _ := ctx.Value(contextKey{}).(*contextGuards)
	for current := guards; current != nil; current = current.parent {
		if err := current.guard.Check(ctx); err != nil {
			return err
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return nil
}

func (g *Guard) Check(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	g.mu.RLock()
	lease := g.lease
	renewErr := g.renewErr
	g.mu.RUnlock()
	if renewErr != nil {
		return renewErr
	}
	if err := g.db.AssertExecutionLease(ctx, lease, g.options.Now()); err != nil {
		g.lose(err)
		return err
	}
	return nil
}

func (g *Guard) Lease() state.ExecutionLease {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.lease
}

func (g *Guard) Close() error {
	g.closeOnce.Do(func() {
		g.stopOnce.Do(func() { close(g.stop) })
		<-g.done
		g.mu.RLock()
		lease := g.lease
		renewErr := g.renewErr
		g.mu.RUnlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		g.closeErr = g.db.ReleaseExecutionLease(ctx, lease, g.options.Now())
		if errors.Is(g.closeErr, state.ErrExecutionLeaseLost) && renewErr != nil {
			g.closeErr = renewErr
		}
	})
	return g.closeErr
}

func (g *Guard) renew(ctx context.Context) {
	defer close(g.done)
	ticker := time.NewTicker(g.options.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.stop:
			return
		case <-ticker.C:
			g.mu.RLock()
			current := g.lease
			g.mu.RUnlock()
			renewed, err := g.db.RenewExecutionLease(context.WithoutCancel(ctx), current, g.options.Now(), g.options.TTL)
			if err != nil {
				g.lose(err)
				return
			}
			g.mu.Lock()
			g.lease = renewed
			g.mu.Unlock()
		}
	}
}

func (g *Guard) lose(err error) {
	g.mu.Lock()
	if g.renewErr == nil {
		g.renewErr = err
	}
	g.mu.Unlock()
	g.cancel(err)
}

func find(ctx context.Context, resourceType, resourceID string) *Guard {
	guards, _ := ctx.Value(contextKey{}).(*contextGuards)
	for current := guards; current != nil; current = current.parent {
		lease := current.guard.Lease()
		if lease.ResourceType == resourceType && lease.ResourceID == resourceID {
			return current.guard
		}
	}
	return nil
}

func normalizeOptions(opts Options) Options {
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.RenewInterval <= 0 {
		opts.RenewInterval = DefaultRenewInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.OwnerID == "" {
		opts.OwnerID = newOwnerID()
	}
	return opts
}

func newOwnerID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("pid-%d-%s", os.Getpid(), hex.EncodeToString(random[:]))
}
