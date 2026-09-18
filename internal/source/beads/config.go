package beads

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

// Config holds the connection settings for the beads Dolt server. It is loaded from the
// environment, so no credential lives in the repo.
type Config struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	EventsTZ string // IANA zone that events.created_at is stored in (spec F1)
}

// Environment variables read by ConfigFromEnv, and their defaults (AC-09).
const (
	envHost     = "BEADS_HOST"
	envPort     = "BEADS_PORT"
	envDatabase = "BEADS_DATABASE"
	envUser     = "BEADS_USER"
	envPassword = "BEADS_PASSWORD"
	envEventsTZ = "BEADS_EVENTS_TZ"

	defaultHost     = "127.0.0.1"
	defaultPort     = 49209
	defaultDatabase = "trk"
	defaultUser     = "root"
	defaultPassword = ""
	defaultEventsTZ = "Australia/Sydney"
)

// ConfigFromEnv loads a Config from BEADS_HOST, BEADS_PORT, BEADS_DATABASE, BEADS_USER,
// BEADS_PASSWORD and BEADS_EVENTS_TZ. An unset variable takes its default; a set one is used as
// given. A BEADS_PORT that is not an integer in 1..65535, or a BEADS_EVENTS_TZ that does not name
// a known time zone, is an error.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Host:     envOr(envHost, defaultHost),
		Port:     defaultPort,
		Database: envOr(envDatabase, defaultDatabase),
		User:     envOr(envUser, defaultUser),
		Password: envOr(envPassword, defaultPassword),
		EventsTZ: envOr(envEventsTZ, defaultEventsTZ),
	}
	if v, ok := os.LookupEnv(envPort); ok {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("beads: %s=%q is not a port number in 1..65535", envPort, v)
		}
		cfg.Port = port
	}
	if _, err := cfg.eventsLocation(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

// eventsLocation resolves EventsTZ. An empty name is rejected rather than read as UTC, since a
// silent UTC fallback would shift every events window by the zone's offset.
func (c Config) eventsLocation() (*time.Location, error) {
	if c.EventsTZ == "" {
		return nil, fmt.Errorf("beads: events time zone (%s) is empty", envEventsTZ)
	}
	loc, err := time.LoadLocation(c.EventsTZ)
	if err != nil {
		return nil, fmt.Errorf("beads: events time zone (%s) %q: %w", envEventsTZ, c.EventsTZ, err)
	}
	return loc, nil
}

// DSN returns the go-sql-driver/mysql data source name for c: TCP, no TLS, parseTime=true so
// DATETIME arrives as time.Time, loc=UTC so those values keep the source's wall-clock fields
// unshifted, and interpolateParams=true so each query is one round trip with no server-side
// prepared statement.
func (c Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true&loc=UTC&interpolateParams=true&tls=false",
		c.User, c.Password, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database)
}
