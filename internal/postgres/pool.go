package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connection pool bounds.
//
// These are set explicitly rather than left to pgx's defaults, which size
// MaxConns from the machine's CPU count — a number that has nothing to do with
// what the database can absorb, and that silently changes when the service moves
// to a different instance type.
const (
	// maxConns caps concurrent server-side connections from one instance.
	maxConns int32 = 20
	// minConns keeps a few connections warm so a burst does not pay dial latency.
	minConns int32 = 2
	// maxConnLifetime recycles connections so a long-lived one cannot pin an old
	// server version or leak session state across a failover.
	maxConnLifetime = 30 * time.Minute
	// maxConnIdleTime returns unused connections to the database.
	maxConnIdleTime = 5 * time.Minute
	// healthCheckPeriod is how often idle connections are probed.
	healthCheckPeriod = 1 * time.Minute
	// connectTimeout bounds a single dial, so startup fails fast on a bad address
	// rather than hanging.
	connectTimeout = 10 * time.Second
)

// NewPool initializes and tests a PostgreSQL connection pool using pgxpool.
//
// Requires: databaseURL is a valid PostgreSQL connection string.
// Ensures:  the returned pool has answered a ping, so a caller that gets no
//
//	error has a working database; on any error nothing is left open.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}

	// Attach the OpenTelemetry tracer to pgx so query spans join the request
	// trace rather than appearing as unattributed database time.
	config.ConnConfig.Tracer = otelpgx.NewTracer()

	config.MaxConns = maxConns
	config.MinConns = minConns
	config.MaxConnLifetime = maxConnLifetime
	config.MaxConnIdleTime = maxConnIdleTime
	config.HealthCheckPeriod = healthCheckPeriod
	config.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	_ = otelpgx.RecordStats(pool)

	return pool, nil
}
