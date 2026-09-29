package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/StevenACoffman/roster/sql/schema"
)

// Migrate applies every pending migration against the given pool.
//
// The logger is a parameter rather than slog.Default() because the default
// writes to the process's own stderr, which bypasses the writer the calling
// command was given and cannot be captured by a test.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return err
	}
	defer cleanup()

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}

	for _, res := range results {
		logger.InfoContext(ctx, "applied migration",
			"version", res.Source.Version,
			"path", res.Source.Path,
			"duration", res.Duration,
		)
	}

	return nil
}

// MigrateDown rolls back the most recent migration. See Migrate on the logger.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return err
	}
	defer cleanup()

	res, err := provider.Down(ctx)
	if err != nil {
		return fmt.Errorf("rolling back migration: %w", err)
	}

	if res != nil {
		logger.InfoContext(ctx, "rolled back migration",
			"version", res.Source.Version,
			"path", res.Source.Path,
			"duration", res.Duration,
		)
	}

	return nil
}

// MigrationStatus reports the state of every available migration.
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return provider.Status(ctx)
}

// GetDBVersion reports the database's current migration version.
func GetDBVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	return provider.GetDBVersion(ctx)
}

// NewProvider builds a goose provider over the embedded migrations.
//
// goose works through database/sql, so the pgx pool is adapted rather than used
// directly. The caller must invoke the returned cleanup to close that adapter.
func NewProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	cleanup := func() { _ = sqlDB.Close() }

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, schema.FS)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("creating goose provider: %w", err)
	}

	return provider, cleanup, nil
}
