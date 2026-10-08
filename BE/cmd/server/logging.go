package main

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// configureLogging selects the logrus formatter and level. LOG_FORMAT=json|text
// wins; otherwise JSON is used everywhere except ENVIRONMENT=development (the
// config default), where human-readable text is kept for local work. LOG_LEVEL
// (debug|info|warn|error, default info) lets a deployed instance surface
// debug lines — e.g. successful health probes and client cancels — without a
// rebuild. It reads the environment directly because it runs before
// config.Load and also covers the migrate subcommand.
func configureLogging() {
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "" {
		environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
		if environment == "" || environment == "development" {
			format = "text"
		} else {
			format = "json"
		}
	}
	if format == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	} else {
		log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
	}

	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))); raw != "" {
		level, err := log.ParseLevel(raw)
		if err != nil {
			log.Warnf("Invalid LOG_LEVEL %q, keeping %s", raw, log.GetLevel())
		} else {
			log.SetLevel(level)
		}
	}
}
