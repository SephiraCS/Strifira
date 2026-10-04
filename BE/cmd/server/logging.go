package main

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// configureLogging selects the logrus formatter. LOG_FORMAT=json|text wins;
// otherwise JSON is used everywhere except ENVIRONMENT=development (the
// config default), where human-readable text is kept for local work. It reads
// the environment directly because it runs before config.Load and also
// covers the migrate subcommand.
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
		return
	}
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
}
