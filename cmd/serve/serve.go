// Package serve implements the "serve" CLI command: the Connect-RPC server for
// the OneRoster rostering API.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/cmd/root"
	"github.com/StevenACoffman/roster/internal/logging"
	"github.com/StevenACoffman/roster/internal/postgres"
	"github.com/StevenACoffman/roster/internal/resilience"
	"github.com/StevenACoffman/roster/internal/telemetry"
)

const (
	// shutdownTimeout for in-flight requests to drain.
	shutdownTimeout = 10 * time.Second
	// readHeaderTimeout is the cheap defence against Slowloris.
	readHeaderTimeout = 5 * time.Second
)

// Config holds the configuration for the serve command.
//
// Every knob the server has is a field here bound to a flag in New. Nothing
// reads os.Getenv: the dispatcher parses with ff.WithEnvVarPrefix("ROSTER"), so
// each flag already has a ROSTER_-prefixed environment variable, and every
// setting stays visible in `roster serve --help`.
type Config struct {
	*root.Config

	Addr             string
	DatabaseURL      string
	AutoMigrate      bool
	CertFile         string
	KeyFile          string
	AllowedOrigins   string
	LogLevel         string
	LogFormat        string
	AdminAddr        string
	DevSubject       string
	RequestTimeout   time.Duration
	TraceSnapshotDir string

	OTelExporter       string
	OTelEndpoint       string
	OTelInsecure       bool
	OTelServiceName    string
	OTelServiceVersion string
	OTelSamplePercent  float64

	RateLimitRPS            uint
	MaxRetries              int
	BreakerFailureThreshold uint
	BreakerOpenDelay        time.Duration

	PyroscopeEndpoint     string
	PyroscopeAuthUser     string
	PyroscopeAuthPassword string
	DeploymentEnvironment string

	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the serve command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("serve").SetParent(parent.Flags)

	cfg.Flags.StringVar(&cfg.Addr, 0, "addr", "127.0.0.1:8080",
		"listen address for the API")
	cfg.Flags.StringVar(&cfg.DatabaseURL, 0, "database-url",
		"postgres://postgres:password@localhost:5432/roster_db?sslmode=disable",
		"PostgreSQL connection string")
	cfg.Flags.BoolVar(&cfg.AutoMigrate, 0, "auto-migrate",
		"apply pending database migrations at startup")
	cfg.Flags.StringVar(&cfg.CertFile, 0, "tls-cert", "",
		"TLS certificate file; serves cleartext when this or --tls-key is unset")
	cfg.Flags.StringVar(&cfg.KeyFile, 0, "tls-key", "",
		"TLS private key file")
	cfg.Flags.StringVar(&cfg.AllowedOrigins, 0, "cors-allowed-origins", "",
		"comma-separated browser origins permitted to call the API")
	cfg.Flags.StringVar(&cfg.LogLevel, 0, "log-level", "info",
		"log level: debug, info, warn, or error")
	cfg.Flags.StringVar(&cfg.LogFormat, 0, "log-format", "json",
		"log format: json or text")
	cfg.Flags.StringVar(&cfg.AdminAddr, 0, "admin-addr", "127.0.0.1:9090",
		`address for pprof and metrics; "off" disables the admin listener`)
	cfg.Flags.StringVar(&cfg.DevSubject, 0, "dev-subject", "",
		"DEVELOPMENT ONLY: authenticate every request as this subject, skipping token checks")
	cfg.Flags.DurationVar(&cfg.RequestTimeout, 0, "request-timeout", 30*time.Second,
		"per-request deadline applied to every RPC")
	cfg.Flags.StringVar(&cfg.TraceSnapshotDir, 0, "trace-snapshot-dir", "",
		"enable the execution-trace flight recorder and write snapshots here")
	cfg.Flags.StringVar(&cfg.OTelExporter, 0, "otel-exporter", telemetry.ExporterNone,
		"trace exporter: none, otlp, or stdout")
	cfg.Flags.StringVar(&cfg.OTelEndpoint, 0, "otel-endpoint", "",
		"OTLP collector address; empty defers to OTEL_EXPORTER_OTLP_ENDPOINT")
	cfg.Flags.BoolVar(&cfg.OTelInsecure, 0, "otel-insecure",
		"send OTLP without TLS, for a collector on the same host")
	cfg.Flags.StringVar(&cfg.OTelServiceName, 0, "otel-service-name", "roster",
		"service name on every span and metric; empty defers to OTEL_SERVICE_NAME")
	cfg.Flags.StringVar(&cfg.OTelServiceVersion, 0, "otel-service-version", "",
		"service version on every span and metric; empty defers to OTEL_SERVICE_VERSION")
	cfg.Flags.Float64Var(&cfg.OTelSamplePercent, 0, "otel-sample-percent", 100,
		"percentage of root spans to record; a sampled remote parent is always recorded")
	cfg.Flags.UintVar(&cfg.RateLimitRPS, 0, "rate-limit-rps",
		resilience.DefaultRateLimitConfig().RequestsPerSecond,
		"requests per second admitted per instance; 0 disables admission control")
	cfg.Flags.IntVar(&cfg.MaxRetries, 0, "db-max-retries",
		resilience.DefaultConfig().MaxRetries,
		"replays of a transient database failure; reads only, never writes")
	cfg.Flags.UintVar(&cfg.BreakerFailureThreshold, 0, "db-breaker-failures",
		resilience.DefaultConfig().FailureThreshold,
		"consecutive infrastructure failures that open the database circuit breaker")
	cfg.Flags.DurationVar(&cfg.BreakerOpenDelay, 0, "db-breaker-open-delay",
		resilience.DefaultConfig().OpenDelay,
		"how long the database circuit breaker stays open before a trial request")
	cfg.Flags.StringVar(&cfg.PyroscopeEndpoint, 0, "pyroscope-endpoint", "",
		"Pyroscope server for continuous profiling; empty disables profiling")
	cfg.Flags.StringVar(&cfg.PyroscopeAuthUser, 0, "pyroscope-auth-user", "",
		"Pyroscope basic-auth user")
	cfg.Flags.StringVar(&cfg.PyroscopeAuthPassword, 0, "pyroscope-auth-password", "",
		"Pyroscope basic-auth password; prefer ROSTER_PYROSCOPE_AUTH_PASSWORD, as a flag value is visible in ps")
	cfg.Flags.StringVar(&cfg.DeploymentEnvironment, 0, "deployment-environment", "",
		`environment label on profiles, e.g. production; empty means "development"`)

	cfg.Command = &ff.Command{
		Name:      "serve",
		Usage:     "roster serve [FLAGS]",
		ShortHelp: "serve the OneRoster rostering API over Connect-RPC",
		LongHelp: `Serve the OneRoster v1.2 rostering API over Connect-RPC, gRPC, and HTTP/JSON.

Every flag can also be set by a ROSTER_-prefixed environment variable: prepend
ROSTER_, uppercase, and replace dashes with underscores, so --database-url
becomes ROSTER_DATABASE_URL. A flag given on the command line wins.

Authentication is by bearer token, hashed and matched against auth_api_token.
Authorization is not applied here: each query joins auth_effective_access so the
database returns only the rows the calling principal is scoped to, whether that
scope is one school or a whole district.

--dev-subject bypasses token checking and authenticates every request as the
named subject. It is refused unless --addr is bound to loopback, because an
exposed listener with it set would serve the roster to anyone who asked.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

// exec wires the service and serves until ctx is cancelled.
//
// Returns root.ExitError only for a failure it has already reported; everything
// else propagates as a normal error for the dispatcher to print.
func (cfg *Config) exec(ctx context.Context, _ []string) error {
	logger := logging.New(cfg.Stderr, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	if err := cfg.validate(); err != nil {
		return err
	}

	// Profiling before telemetry: whether the profiler is running is what decides
	// whether a span should carry a profile id, so it has to be known first.
	stopProfiling := cfg.startProfiling(ctx, logger)
	defer stopProfiling()

	// Telemetry next: otelconnect and otelpgx both resolve the global providers
	// when they are built, so a provider installed after them is never seen.
	metrics, shutdownTelemetry := cfg.startTelemetry(ctx, logger)
	defer shutdownTelemetry()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("serve: connecting to database: %w", err)
	}
	defer pool.Close()
	logger.InfoContext(ctx, "connected to postgresql")

	if cfg.AutoMigrate {
		if migrateErr := postgres.Migrate(ctx, pool, logger); migrateErr != nil {
			return fmt.Errorf("serve: applying migrations: %w", migrateErr)
		}
	} else {
		logger.InfoContext(ctx, "skipping automatic migrations", "auto_migrate", false)
	}

	handler, err := cfg.newServerHandler(pool, logger)
	if err != nil {
		return fmt.Errorf("serve: building server handler: %w", err)
	}

	admin, err := cfg.startAdmin(ctx, logger, metrics)
	if err != nil {
		return fmt.Errorf("serve: starting admin listener: %w", err)
	}
	if admin != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if closeErr := admin.Close(shutdownCtx); closeErr != nil {
				logger.ErrorContext(ctx, "shutting down admin listener", "error", closeErr)
			}
		}()
	}

	return cfg.listenAndServe(ctx, logger, handler)
}

// validate rejects a configuration that would serve the roster to the wrong
// people, or that cannot serve it at all.
func (cfg *Config) validate() error {
	if cfg.DevSubject != "" && !isLoopback(cfg.Addr) {
		return fmt.Errorf(
			"serve: --dev-subject disables authentication and requires a loopback --addr, got %q",
			cfg.Addr)
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return errors.New("serve: --tls-cert and --tls-key must be given together")
	}
	return nil
}

// listenAndServe runs the API listener until ctx is cancelled, then drains.
func (cfg *Config) listenAndServe(
	ctx context.Context, logger *slog.Logger, handler http.Handler,
) error {
	// HTTP/1.1, HTTP/2 over TLS, and h2c, so a gRPC client works without TLS in
	// development and a browser works over either.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	var listenCfg net.ListenConfig
	listener, err := listenCfg.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("serve: listening on %s: %w", cfg.Addr, err)
	}
	defer func() { _ = listener.Close() }()

	useTLS := cfg.CertFile != "" && cfg.KeyFile != ""
	scheme := "http"
	if useTLS {
		scheme = "https"
	}

	logger.InfoContext(ctx, "roster api listening",
		"url", fmt.Sprintf("%s://%s", scheme, listener.Addr()),
		"tls", useTLS,
		"dev_subject", cfg.DevSubject != "",
	)
	// The resolved address goes to stdout so a caller that asked for :0 — a test,
	// or a script picking a free port — can discover which port it got.
	_, _ = fmt.Fprintf(cfg.Stdout, "listening on %s://%s\n", scheme, listener.Addr())

	serveErr := make(chan error, 1)
	go func() {
		var err error
		if useTLS {
			err = srv.ServeTLS(listener, cfg.CertFile, cfg.KeyFile)
		} else {
			err = srv.Serve(listener)
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.InfoContext(ctx, "shutting down")
	// WithoutCancel, not Background: ctx is already cancelled, but in-flight
	// requests still get shutdownTimeout to finish.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("serve: graceful shutdown: %w", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	logger.InfoContext(ctx, "server exited cleanly")
	return nil
}

// isLoopback reports whether addr binds only to the loopback interface.
//
// A bare port (":8080") or an empty host binds every interface, so both are
// treated as non-loopback — the safe reading, since that is what makes
// --dev-subject reachable from off-host.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startAdmin brings up the admin listener, or returns nil when it is disabled.
//
// A nil metricsHandler omits /metrics; everything else about the listener is
// unchanged, so metrics being unavailable does not cost the operator pprof.
func (cfg *Config) startAdmin(
	ctx context.Context, logger *slog.Logger, metricsHandler http.Handler,
) (*adminServer, error) {
	if cfg.AdminAddr == "" || cfg.AdminAddr == "off" {
		return nil, nil
	}

	admin, err := newAdminServer(ctx, logger, cfg.AdminAddr, cfg.TraceSnapshotDir, metricsHandler)
	if err != nil {
		return nil, err
	}

	logger.InfoContext(ctx, "admin listener started",
		"addr", admin.Addr().String(),
		"metrics", metricsHandler != nil,
		"flight_recorder", cfg.TraceSnapshotDir != "",
	)
	go func() {
		if serveErr := admin.Serve(); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			logger.ErrorContext(ctx, "admin listener stopped", "error", serveErr)
		}
	}()
	return admin, nil
}
