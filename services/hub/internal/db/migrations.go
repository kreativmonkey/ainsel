package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate runs all up migrations against the given DSN. It is idempotent —
// re-running with no new files is a no-op. Safe to call at every startup.
//
// After migrating it verifies the per-version checksum history in
// schema_history, so a migration that was renumbered or edited after being
// deployed fails startup loudly instead of leaving a silent schema hole.
func Migrate(ctx context.Context, dsn string) error {
	if !looksLikeURL(dsn) {
		return fmt.Errorf("migrate: DSN must be in URL form (postgres://...), got %q", dsn)
	}
	files, err := loadMigrationSet()
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}
	if err := checkMigrationSource(files); err != nil {
		return err
	}
	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}
	// The pgx/v5 driver registers itself under "pgx5"; use that scheme.
	pgxDSN := "pgx5://" + trimScheme(dsn)
	m, err := migrate.NewWithSourceInstance("iofs", src, pgxDSN)
	if err != nil {
		return fmt.Errorf("migrate new: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("migrate version: %w", err)
	}
	if dirty {
		return fmt.Errorf("migrate: database is at version %d in a dirty state; resolve the failed migration before starting", version)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("migrate history pool: %w", err)
	}
	defer pool.Close()
	if err := verifyAndRecordHistory(ctx, pool, files, int(version)); err != nil {
		return err
	}
	return nil
}

// trimScheme strips an existing "postgres://" or "postgresql://" prefix so we
// can replace it with "pgx5://" for golang-migrate's driver name.
func trimScheme(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://", "pgx5://"} {
		if len(dsn) >= len(prefix) && dsn[:len(prefix)] == prefix {
			return dsn[len(prefix):]
		}
	}
	return dsn
}

// looksLikeURL returns true if dsn starts with a recognized scheme prefix.
// We require URL-form DSNs because trimScheme (and golang-migrate's source
// driver detection) only handles those.
func looksLikeURL(dsn string) bool {
	for _, prefix := range []string{"postgres://", "postgresql://", "pgx5://"} {
		if strings.HasPrefix(dsn, prefix) {
			return true
		}
	}
	return false
}
