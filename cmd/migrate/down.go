package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/cmd/root"
	"github.com/StevenACoffman/roster/internal/postgres"
)

// downConfig holds the configuration for "roster migrate down".
type downConfig struct {
	*Config

	Confirm bool

	Flags   *ff.FlagSet
	Command *ff.Command
}

// newDown registers the down subcommand under the migrate group.
func newDown(parent *Config) *downConfig {
	var cfg downConfig
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("down").SetParent(parent.Flags)

	// A rollback drops tables and the data in them, and unlike "up" it cannot be
	// undone by running it again. The flag makes the destruction deliberate
	// rather than one shell-history arrow-key away.
	cfg.Flags.BoolVar(&cfg.Confirm, 0, "confirm",
		"required: acknowledge that rolling back discards data in the affected tables")

	cfg.Command = &ff.Command{
		Name:      "down",
		Usage:     "roster migrate down --confirm [FLAGS]",
		ShortHelp: "roll back the most recent migration",
		LongHelp: `Roll back the single most recent migration.

This runs that migration's Down section, which drops the tables and columns the
Up section created. The data in them is discarded and this command cannot bring
it back — take a backup first if the data matters.

--confirm is required. Without it the command reports what it would roll back
and exits non-zero, so that a rollback is never the result of a mistyped
command.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *downConfig) exec(ctx context.Context, _ []string) error {
	return cfg.withPool(ctx, func(ctx context.Context, pool *pgxpool.Pool) error {
		before, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate down: reading current version: %w", err)
		}

		if before == 0 {
			_, _ = fmt.Fprintln(cfg.Stdout, "no migrations applied; nothing to roll back")
			return nil
		}

		if !cfg.Confirm {
			_, _ = fmt.Fprintf(cfg.Stderr,
				"refusing to roll back version %d without --confirm; it discards the data in that migration's tables\n",
				before)
			// ExitError rather than a returned error: the reason is already on
			// stderr in the operator's terms, and the dispatcher would otherwise
			// print it again as "error: ...".
			return root.ExitError(1)
		}

		if downErr := postgres.MigrateDown(ctx, pool, cfg.logger()); downErr != nil {
			return fmt.Errorf("migrate down: %w", downErr)
		}

		after, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate down: reading resulting version: %w", err)
		}

		_, _ = fmt.Fprintf(cfg.Stdout, "rolled back from version %d to %d\n", before, after)
		return nil
	})
}
