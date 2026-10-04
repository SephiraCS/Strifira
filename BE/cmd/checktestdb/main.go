// checktestdb refuses a TEST_DATABASE_URL that names the application
// database (APP_DATABASE_URL), comparing what pgx resolves from both: every
// host and port it would try (fallbacks included) and the database name, so
// percent-encoding, keyword DSNs in any order and query overrides cannot
// hide a match. localhost, 127.0.0.1 and ::1 count as one host. Messages
// carry only host:port/database, never credentials or the raw strings.
//
// Run by scripts/ci/check-test-database.sh before `make test-migrate`.
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

type target struct {
	host     string
	port     uint16
	database string
}

func (t target) String() string {
	return net.JoinHostPort(t.host, strconv.Itoa(int(t.port))) + "/" + t.database
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return "loopback"
	}
	return host
}

// targets lists every host:port/database pgx would connect to.
func targets(name, connString string) ([]target, error) {
	config, err := pgconn.ParseConfig(connString)
	if err != nil {
		// The parse error may quote the connection string; keep it out.
		return nil, fmt.Errorf("%s is not a valid PostgreSQL connection string", name)
	}
	out := []target{{normalizeHost(config.Host), config.Port, config.Database}}
	for _, fallback := range config.Fallbacks {
		out = append(out, target{normalizeHost(fallback.Host), fallback.Port, config.Database})
	}
	return out, nil
}

func run() error {
	test := os.Getenv("TEST_DATABASE_URL")
	if test == "" {
		return fmt.Errorf("TEST_DATABASE_URL is not set: point it at a test database (never the app database)")
	}
	app := os.Getenv("APP_DATABASE_URL")
	if app == "" {
		return nil
	}
	testTargets, err := targets("TEST_DATABASE_URL", test)
	if err != nil {
		return err
	}
	appTargets, err := targets("the .env DATABASE_URL", app)
	if err != nil {
		return err
	}
	for _, t := range testTargets {
		for _, a := range appTargets {
			if t == a {
				return fmt.Errorf("TEST_DATABASE_URL names the application database from .env (%s); "+
					"the backend suites write to it. Point TEST_DATABASE_URL at a separate test database", t)
			}
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
