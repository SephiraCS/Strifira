package main

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
)

// shutdownSteps are the parts of the server stopped on SIGINT/SIGTERM.
type shutdownSteps struct {
	// stopBackground cancels the background producers' lifecycle context.
	stopBackground func()
	// waitBackground waits for the producers' in-flight iteration.
	waitBackground func(context.Context) error
	// drainHTTP stops accepting and drains regular HTTP requests.
	drainHTTP func(context.Context) error
	// drainWebSocket closes hijacked WebSockets and waits for their cleanup.
	drainWebSocket func(context.Context) error
	// closeWebhooks flushes the outgoing webhook delivery pool.
	closeWebhooks func(context.Context) error
}

// gracefulShutdown stops the server within ctx's deadline:
//   - background producers are cancelled right away;
//   - HTTP and WebSocket draining run concurrently, so a stalled request
//     cannot eat the sockets' grace period;
//   - the webhook pool is closed only after both HTTP draining and the
//     producers have finished, so events emitted by drained requests and by
//     a producer's last iteration are still delivered rather than dropped.
func gracefulShutdown(ctx context.Context, steps shutdownSteps) {
	steps.stopBackground()
	var wg sync.WaitGroup
	wg.Go(func() {
		var producers sync.WaitGroup
		producers.Go(func() {
			if err := steps.waitBackground(ctx); err != nil {
				log.WithError(err).Warn("background workers did not stop in time")
			}
		})
		if err := steps.drainHTTP(ctx); err != nil {
			log.WithError(err).Warn("graceful shutdown did not complete")
		}
		producers.Wait()
		if err := steps.closeWebhooks(ctx); err != nil {
			log.WithError(err).Warn("webhook delivery queue did not drain")
		}
	})
	wg.Go(func() {
		if err := steps.drainWebSocket(ctx); err != nil {
			log.WithError(err).Warn("websocket shutdown did not complete")
		}
	})
	wg.Wait()
}
