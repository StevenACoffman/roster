// Package migrate implements the "migrate" CLI command group: applying,
// rolling back, and reporting on the database schema.
package migrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/cmd/root"
	"github.com/StevenACoffman/roster/internal/logging"
	"github.com/StevenACoffman/roster/internal/postgres"
)

// defaultDatabaseURL matches the docker-compose service, so a local checkout
// needs no flag. It is the same default the serve command uses; the two are
// deliberately identical strings rather than a shared constant, because sharing
// one would make cmd/serve and cmd/migrate import each other.
const defaultDatabaseURL = "postgres://postgres:password@localhost:5432/roster_db?sslmode=disable"

// Config holds the configuration shared by the migrate subcommands.
//
// The subcommands embed this rather than root.Config, so --database-url is
// declared once and inherited by each of them through SetParent.
type Config struct {
	*root.Config

	DatabaseURL string
	LogLevel    string
	LogFormat   string

	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the migrate command group with the given parent.
//
// Exec is deliberately unset: a bare `roster migrate` is not an operation, and a
// group parent without Exec returns ff.ErrNoExec, which the dispatcher reports
// as usage rather than as a failure.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("migrate").SetParent(parent.Flags)

	cfg.Flags.StringVar(&cfg.DatabaseURL, 0, "database-url", defaultDatabaseURL,
		"PostgreSQL connection string")
	cfg.Flags.StringVar(&cfg.LogLevel, 0, "log-level", "info",
		"log level: debug, info, warn, or error")
	cfg.Flags.StringVar(&cfg.LogFormat, 0, "log-format", "text",
		"log format: json or text")

	cfg.Command = &ff.Command{
		Name:      "migrate",
		Usage:     "roster migrate <SUBCOMMAND> [FLAGS]",
		ShortHelp: "apply, roll back, and report on database migrations",
		LongHelp: `Manage the database schema embedded in this binary.

The migrations in sql/schema are compiled into the binary, so the version of the
schema this command applies is always the version this binary was built against.
There is no way for it to apply a migration the code does not know about.

Every flag can also be set by a ROSTER_-prefixed environment variable:
--database-url becomes ROSTER_DATABASE_URL.

  roster migrate status   report which migrations are applied
  roster migrate up       apply every pending migration
  roster migrate down     roll back the most recent migration
  roster migrate version  print the database's current schema version

"roster serve --auto-migrate" applies migrations at startup, which is convenient
in development. Prefer running "migrate up" as a separate step in production, so
a failed migration is a failed deploy step rather than a crash-looping service.`,
		Flags: cfg.Flags,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)

	newUp(&cfg)
	newDown(&cfg)
	newStatus(&cfg)
	newVersion(&cfg)

	return &cfg
}

// logger builds the migration logger over the command's own stderr, so its
// output is captured wherever the command's writers point rather than escaping
// to the process's stderr.
func (cfg *Config) logger() *slog.Logger {
	return logging.New(cfg.Stderr, cfg.LogLevel, cfg.LogFormat)
}

// withPool opens a pool, runs op against it, and closes it.
//
// Every subcommand needs exactly this, and none of them should be able to forget
// the close — so the pool is not reachable except through here.
func (cfg *Config) withPool(ctx context.Context, op func(context.Context, *pgxpool.Pool) error) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	return op(ctx, pool)
}
