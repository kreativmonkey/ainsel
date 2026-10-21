package db_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ainsel-project/ainsel/services/hub/internal/db"
)

// TestMigrateRecordsAppliedMigrationHistory checks that a migrated database
// carries a per-version checksum row for every applied migration, so later
// renumbering can be detected. Skips if Docker is unavailable.
func TestMigrateRecordsAppliedMigrationHistory(t *testing.T) {
	dsn, stop := startPostgres(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var recorded, maxVersion int
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(max(version), 0) FROM schema_history`).Scan(&recorded, &maxVersion); err != nil {
		t.Fatalf("read schema_history: %v", err)
	}
	if maxVersion == 0 {
		t.Fatal("schema_history is empty after Migrate")
	}
	if recorded != maxVersion {
		t.Errorf("schema_history has %d rows for %d applied versions", recorded, maxVersion)
	}

	// Recording must stay idempotent — Migrate runs on every hub startup.
	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var again int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_history`).Scan(&again); err != nil {
		t.Fatalf("re-read schema_history: %v", err)
	}
	if again != recorded {
		t.Errorf("second Migrate changed schema_history row count: %d -> %d", recorded, again)
	}
}

// TestMigrateDetectsRenumberedMigration is the regression test for the incident
// where migration 0026 was renumbered to 0027 after deployment: the database was
// already stamped at the highest version, so the new 0026 was skipped in silence
// and every request that read the missing column failed with a 500.
func TestMigrateDetectsRenumberedMigration(t *testing.T) {
	dsn, stop := startPostgres(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var version int
	var checksum string
	if err := pool.QueryRow(ctx, `SELECT version, checksum FROM schema_history ORDER BY version DESC LIMIT 1`).Scan(&version, &checksum); err != nil {
		t.Fatalf("read newest row: %v", err)
	}
	// Stand in for "this version was applied from different file contents".
	if _, err := pool.Exec(ctx, `UPDATE schema_history SET checksum = $1 WHERE version = $2`, strings.Repeat("0", 64), version); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}

	err = db.Migrate(ctx, dsn)
	if err == nil {
		t.Fatal("expected drift error after the newest migration's recorded checksum changed")
	}
	var drift *db.HistoryDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("expected *db.HistoryDriftError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(version)) {
		t.Errorf("error should name the drifting version %d, got: %v", version, err)
	}

	// The drift must not be papered over: the recorded row stays as-is so the
	// operator can see what was applied.
	var still string
	if err := pool.QueryRow(ctx, `SELECT checksum FROM schema_history WHERE version = $1`, version).Scan(&still); err != nil {
		t.Fatalf("re-read tampered row: %v", err)
	}
	if still != strings.Repeat("0", 64) {
		t.Errorf("tampered checksum was overwritten: %s", still)
	}
}
