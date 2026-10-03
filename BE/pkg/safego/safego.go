// Package safego runs background work without letting a panic take down the
// whole process. A recovered panic is logged with its stack trace.
package safego

import (
	"fmt"
	"runtime/debug"

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
