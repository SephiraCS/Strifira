// Package safego runs background work without letting a panic take down the
// whole process. A recovered panic is logged with its stack trace.
package safego

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Go starts fn in a new goroutine named name for logging purposes. A panic in
// fn is recovered and logged; the goroutine then exits.
func Go(name string, fn func()) {
	go Run(name, fn)
}

// Run calls fn synchronously and recovers a panic, logging it with the stack.
// Use it for each iteration of a long-lived loop so a single bad tick does
// not stop the loop. It reports whether fn panicked.
func Run(name string, fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			log.WithFields(log.Fields{
				"goroutine": name,
				"panic":     fmt.Sprintf("%v", r),
				"stack":     string(debug.Stack()),
			}).Error("panic recovered in background goroutine")
		}
	}()
	fn()
	return false
}

// Group tracks background goroutines so shutdown can wait for them. Each
// goroutine is recovered like Go. Once Wait has been called the group is
// closed: later Go calls are refused, so nothing new starts mid-shutdown.
type Group struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// Go starts fn in a tracked, recovered goroutine. It reports false (and does
// not run fn) when the group is already closed.
func (g *Group) Go(name string, fn func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		Run(name, fn)
	}()
	return true
}

// Wait closes the group and waits until every tracked goroutine returned,
// or until ctx ends (returning its error). Callers cancel the goroutines'
// context first; Wait only bounds how long their in-flight work may take.
func (g *Group) Wait(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Every runs fn every interval (first right away when immediate) until ctx
// is done. Each run is recovered, so one bad tick does not stop the loop.
func Every(ctx context.Context, name string, interval time.Duration, immediate bool, fn func(context.Context)) {
	if immediate {
		Run(name, func() { fn(ctx) })
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			Run(name, func() { fn(ctx) })
		}
	}
}
