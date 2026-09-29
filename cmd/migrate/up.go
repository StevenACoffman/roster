package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/internal/postgres"
)

// upConfig holds the configuration for "roster migrate up".
type upConfig struct {
	*Config

	Flags   *ff.FlagSet
	Command *ff.Command
}

// newUp registers the up subcommand under the migrate group.
func newUp(parent *Config) *upConfig {
	var cfg upConfig
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("up").SetParent(parent.Flags)
	cfg.Command = &ff.Command{
		Name:      "up",
		Usage:     "roster migrate up [FLAGS]",
		ShortHelp: "apply every pending migration",
		LongHelp: `Apply every migration the database has not yet run, in version order.

Each migration runs in its own transaction, so a failure leaves the database at
the last version that applied cleanly rather than part-way through one. Applying
when nothing is pending is a no-op and succeeds.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *upConfig) exec(ctx context.Context, _ []string) error {
	return cfg.withPool(ctx, func(ctx context.Context, pool *pgxpool.Pool) error {
		before, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate up: reading current version: %w", err)
		}

		if migrateErr := postgres.Migrate(ctx, pool, cfg.logger()); migrateErr != nil {
			return fmt.Errorf("migrate up: %w", migrateErr)
		}

		after, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate up: reading resulting version: %w", err)
		}

		if before == after {
			_, _ = fmt.Fprintf(cfg.Stdout, "already up to date at version %d\n", after)
			return nil
		}
		_, _ = fmt.Fprintf(cfg.Stdout, "migrated from version %d to %d\n", before, after)
		return nil
	})
}
