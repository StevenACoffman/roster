package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/internal/postgres"
)

// versionConfig holds the configuration for "roster migrate version".
type versionConfig struct {
	*Config

	Flags   *ff.FlagSet
	Command *ff.Command
}

// newVersion registers the version subcommand under the migrate group.
//
// Distinct from the top-level "roster version", which reports the binary's build
// metadata. This one reports the database's schema version — a different fact
// about a different thing that happens to share a name.
func newVersion(parent *Config) *versionConfig {
	var cfg versionConfig
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("version").SetParent(parent.Flags)
	cfg.Command = &ff.Command{
		Name:      "version",
		Usage:     "roster migrate version [FLAGS]",
		ShortHelp: "print the database's current schema version",
		LongHelp: `Print the version of the most recently applied migration, and nothing else.

The bare number on stdout is the point: a deploy script can compare it against
an expected version without parsing "migrate status". A database with no
migrations applied prints 0.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *versionConfig) exec(ctx context.Context, _ []string) error {
	return cfg.withPool(ctx, func(ctx context.Context, pool *pgxpool.Pool) error {
		version, err := postgres.GetDBVersion(ctx, pool)
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		_, _ = fmt.Fprintf(cfg.Stdout, "%d\n", version)
		return nil
	})
}
