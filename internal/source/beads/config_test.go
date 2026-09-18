package beads_test

// Behavioral tests for beads.Config / ConfigFromEnv / Config.DSN, anchored to AC-09 ("Config from
// environment") in docs/fable-streams/2026-09-18-phase-1-build/spec.md and the exact-API block of
// tasks/03-beads-fetcher/brief.md ("tcp, no TLS, parseTime=true, loc=UTC, interpolateParams=true").
//
// Black-box (package beads_test): only beads's exported surface is used.

import (
	"os"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/DMokong/data-platform/internal/source/beads"
)

// beadsEnvVars are every environment variable AC-09 names.
var beadsEnvVars = []string{
	"BEADS_HOST", "BEADS_PORT", "BEADS_DATABASE", "BEADS_USER", "BEADS_PASSWORD", "BEADS_EVENTS_TZ",
}

// setEnv sets an environment variable for the duration of t, using os.Setenv directly (rather
// than testing.T.Setenv) so it composes with clearBeadsEnv's own restore-on-cleanup below without
// double-registering cleanups for the same variable.
func setEnv(t *testing.T, name, value string) {
	t.Helper()
	if err := os.Setenv(name, value); err != nil {
		t.Fatalf("os.Setenv(%q, %q): %v", name, value, err)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("os.Unsetenv(%q): %v", name, err)
	}
}

// clearBeadsEnv unsets every BEADS_* variable ConfigFromEnv reads, restoring each var's original
// value (present or absent) once the test ends, so tests never leak state into one another or
// into a developer's real shell.
func clearBeadsEnv(t *testing.T) {
	t.Helper()
	for _, name := range beadsEnvVars {
		orig, had := os.LookupEnv(name)
		unsetEnv(t, name)
		t.Cleanup(func() {
			if had {
				setEnv(t, name, orig)
			} else {
				unsetEnv(t, name)
			}
		})
	}
}

// AC-09: with every BEADS_* variable unset, ConfigFromEnv returns the documented defaults:
// 127.0.0.1, 49209, trk, root, empty password, Australia/Sydney.
func TestConfigFromEnv_Defaults_AC09(t *testing.T) {
	clearBeadsEnv(t)

	got, err := beads.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with no BEADS_* vars set returned error: %v", err)
	}
	want := beads.Config{
		Host:     "127.0.0.1",
		Port:     49209,
		Database: "trk",
		User:     "root",
		Password: "",
		EventsTZ: "Australia/Sydney",
	}
	if got != want {
		t.Errorf("ConfigFromEnv() = %+v, want defaults %+v", got, want)
	}
}

// AC-09: every BEADS_* variable, when set, overrides its default.
func TestConfigFromEnv_Overrides_AC09(t *testing.T) {
	clearBeadsEnv(t)
	setEnv(t, "BEADS_HOST", "db.internal")
	setEnv(t, "BEADS_PORT", "3307")
	setEnv(t, "BEADS_DATABASE", "trk_test")
	setEnv(t, "BEADS_USER", "reader")
	setEnv(t, "BEADS_PASSWORD", "s3cret")
	setEnv(t, "BEADS_EVENTS_TZ", "UTC")

	got, err := beads.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with overrides returned error: %v", err)
	}
	want := beads.Config{
		Host:     "db.internal",
		Port:     3307,
		Database: "trk_test",
		User:     "reader",
		Password: "s3cret",
		EventsTZ: "UTC",
	}
	if got != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", got, want)
	}
}

// AC-09: a single overridden variable leaves the rest at their defaults (proves each field is
// read independently, not as an all-or-nothing block).
func TestConfigFromEnv_PartialOverride_AC09(t *testing.T) {
	clearBeadsEnv(t)
	setEnv(t, "BEADS_DATABASE", "trk_staging")

	got, err := beads.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with one override returned error: %v", err)
	}
	if got.Database != "trk_staging" {
		t.Errorf("Database = %q, want the overridden %q", got.Database, "trk_staging")
	}
	if got.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want default %q (unaffected by BEADS_DATABASE)", got.Host, "127.0.0.1")
	}
	if got.Port != 49209 {
		t.Errorf("Port = %d, want default %d (unaffected by BEADS_DATABASE)", got.Port, 49209)
	}
	if got.EventsTZ != "Australia/Sydney" {
		t.Errorf("EventsTZ = %q, want default %q (unaffected by BEADS_DATABASE)", got.EventsTZ, "Australia/Sydney")
	}
}

// AC-09: "invalid port ... -> error." A non-numeric BEADS_PORT must not silently become 0 or some
// truncated value; ConfigFromEnv must reject it.
func TestConfigFromEnv_InvalidPort_AC09(t *testing.T) {
	for _, bad := range []string{"not-a-number", "", "49209.5", "-1x", "49209 "} {
		t.Run(bad, func(t *testing.T) {
			clearBeadsEnv(t)
			setEnv(t, "BEADS_PORT", bad)
			if _, err := beads.ConfigFromEnv(); err == nil {
				t.Errorf("ConfigFromEnv() with BEADS_PORT=%q returned nil error, want a rejection", bad)
			}
		})
	}
}

// AC-09: "unknown tz -> error." A BEADS_EVENTS_TZ that time.LoadLocation cannot resolve must be
// rejected rather than silently accepted and only failing later inside a query.
func TestConfigFromEnv_UnknownTimezone_AC09(t *testing.T) {
	clearBeadsEnv(t)
	setEnv(t, "BEADS_EVENTS_TZ", "Not/AZone")
	if _, err := beads.ConfigFromEnv(); err == nil {
		t.Error("ConfigFromEnv() with an unknown BEADS_EVENTS_TZ returned nil error, want a rejection")
	}
}

// AC-09: Config.DSN() produces a go-sql-driver/mysql DSN that actually carries the documented
// connection properties -- parsed back with the driver's own mysql.ParseDSN rather than matched
// against a hand-picked substring, so this test would catch a DSN that merely *looks* right
// (wrong parameter name, wrong escaping) as well as one that is missing a property outright.
func TestConfigDSN_AC09(t *testing.T) {
	cfg := beads.Config{
		Host:     "127.0.0.1",
		Port:     49209,
		Database: "trk",
		User:     "root",
		Password: "s3cret!@#",
		EventsTZ: "Australia/Sydney",
	}

	parsed, err := mysql.ParseDSN(cfg.DSN())
	if err != nil {
		t.Fatalf("mysql.ParseDSN(cfg.DSN()) = %v; DSN() produced an unparseable DSN %q", err, cfg.DSN())
	}
	if parsed.Net != "tcp" {
		t.Errorf("DSN Net = %q, want %q", parsed.Net, "tcp")
	}
	if want := "127.0.0.1:49209"; parsed.Addr != want {
		t.Errorf("DSN Addr = %q, want %q", parsed.Addr, want)
	}
	if parsed.DBName != "trk" {
		t.Errorf("DSN DBName = %q, want %q", parsed.DBName, "trk")
	}
	if parsed.User != "root" {
		t.Errorf("DSN User = %q, want %q", parsed.User, "root")
	}
	if parsed.Passwd != "s3cret!@#" {
		t.Errorf("DSN Passwd = %q, want %q (password round-trips through DSN escaping)", parsed.Passwd, "s3cret!@#")
	}
	if !parsed.ParseTime {
		t.Error("DSN ParseTime = false, want true")
	}
	if !parsed.InterpolateParams {
		t.Error("DSN InterpolateParams = false, want true")
	}
	if parsed.Loc == nil || parsed.Loc.String() != "UTC" {
		t.Errorf("DSN Loc = %v, want UTC", parsed.Loc)
	}
	if parsed.TLSConfig == "true" || parsed.TLSConfig == "skip-verify" || parsed.TLSConfig == "preferred" || parsed.TLS != nil {
		t.Errorf("DSN enables TLS (TLSConfig=%q, TLS=%v), want no TLS", parsed.TLSConfig, parsed.TLS)
	}
}

// AC-09: DSN() never embeds a literal secret name a reviewer would mistake for a committed
// credential -- i.e. it is built from cfg's own fields, not a hard-coded password. This calls DSN
// twice with two different passwords and checks the DSN actually changes, which would fail if DSN
// ignored cfg.Password (e.g. a hard-coded empty password).
func TestConfigDSN_UsesConfigPassword_AC09(t *testing.T) {
	base := beads.Config{Host: "127.0.0.1", Port: 49209, Database: "trk", User: "root", EventsTZ: "UTC"}
	withPW := base
	withPW.Password = "hunter2"

	if base.DSN() == withPW.DSN() {
		t.Error("DSN() is identical with and without a password set; want DSN to reflect Config.Password")
	}
}
