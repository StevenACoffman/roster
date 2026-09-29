package migrate

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/peterbourgon/ff/v4"
	"github.com/pressly/goose/v3"

	"github.com/StevenACoffman/roster/internal/postgres"
)

// statusConfig holds the configuration for "roster migrate status".
type statusConfig struct {
	*Config

	Flags   *ff.FlagSet
	Command *ff.Command
}

// newStatus registers the status subcommand under the migrate group.
func newStatus(parent *Config) *statusConfig {
	var cfg statusConfig
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("status").SetParent(parent.Flags)
	cfg.Command = &ff.Command{
		Name:      "status",
		Usage:     "roster migrate status [FLAGS]",
		ShortHelp: "report which migrations are applied",
		LongHelp: `List every migration compiled into this binary and whether the database has run it.

A migration shown as pending is one "roster migrate up" would apply. A database
at a higher version than this binary knows about is the signal that the binary
is older than the schema — roll the deployment forward rather than running down.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *statusConfig) exec(ctx context.Context, _ []string) error {
	return cfg.withPool(ctx, func(ctx context.Context, pool *pgxpool.Pool) error {
		statuses, err := postgres.MigrationStatus(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate status: %w", err)
		}

		version, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate status: reading current version: %w", err)
		}

		w := tabwriter.NewWriter(cfg.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "VERSION\tSTATE\tAPPLIED AT\tSOURCE")
		for _, s := range statuses {
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
				s.Source.Version, s.State, appliedAt(s), s.Source.Path)
		}
		_ = w.Flush()

		_, _ = fmt.Fprintf(cfg.Stdout, "\ndatabase is at version %d\n", version)
		return nil
	})
}

// appliedAt renders a migration's application time, or a dash when it has not
// run. goose leaves AppliedAt at its zero value for a pending migration, which
// would otherwise print as year 1.
func appliedAt(s *goose.MigrationStatus) string {
	if s.State != goose.StateApplied || s.AppliedAt.IsZero() {
		return "-"
	}
	return s.AppliedAt.UTC().Format(time.RFC3339)
}
