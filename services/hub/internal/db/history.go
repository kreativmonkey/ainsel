package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaHistoryTable records every migration version this database has applied,
// together with the checksum of the migration file as it was when it was
// applied.
//
// golang-migrate tracks only the *highest* applied version in
// schema_migrations. That single number cannot represent a non-contiguous
// history: if a migration file is renumbered or edited after it has been
// deployed, `Up()` sees a database that is already at the newest version and
// silently applies nothing — leaving a permanent, invisible hole in the schema
// while the hub starts up looking healthy.
//
// Per-version checksums close that gap: a version that was applied from
// different file contents than the ones shipping now is reported as drift and
// refuses to start the hub.
const schemaHistoryTable = "schema_history"

const schemaHistoryDDL = `CREATE TABLE IF NOT EXISTS ` + schemaHistoryTable + ` (
	version    BIGINT       PRIMARY KEY,
	checksum   TEXT         NOT NULL,
	applied_at TIMESTAMPTZ  NOT NULL DEFAULT now()
)`

// migrationSet maps a migration version to the checksum of its up.sql.
type migrationSet map[int]string

// checksumSQL returns the hex sha256 of migration SQL bytes.
func checksumSQL(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// loadMigrationSet reads the embedded up migrations into a version -> checksum
// map. Down migrations are not part of the applied history and are ignored.
func loadMigrationSet() (migrationSet, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	set := migrationSet{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		version, err := migrationVersion(name)
		if err != nil {
			return nil, err
		}
		b, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if _, dup := set[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s)", version, name)
		}
		set[version] = checksumSQL(b)
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("no migrations found in embedded FS")
	}
	return set, nil
}

// migrationVersion parses the leading NNNN_ prefix of a migration filename.
func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: expected NNNN_name.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad version prefix: %w", name, err)
	}
	if v < 1 {
		return 0, fmt.Errorf("migration %q: version must be >= 1", name)
	}
	return v, nil
}

// missingVersions returns the versions absent from 1..max of the set. An empty
// result means the set is contiguous.
func missingVersions(set migrationSet) []int {
	if len(set) == 0 {
		return nil
	}
	vs := set.versions()
	max := vs[len(vs)-1]
	var missing []int
	for v := 1; v <= max; v++ {
		if _, ok := set[v]; !ok {
			missing = append(missing, v)
		}
	}
	return missing
}

func (m migrationSet) versions() []int {
	vs := make([]int, 0, len(m))
	for v := range m {
		vs = append(vs, v)
	}
	sort.Ints(vs)
	return vs
}

// historyDrift is the difference between what a database claims to have applied
// and what the shipping migration files say. An empty result means consistent.
type historyDrift struct {
	// Mismatched are applied versions whose recorded checksum differs from the
	// file shipping now — the file was edited or renumbered after deployment.
	Mismatched []int
	// ToRecord are applied versions with no recorded row yet. These are trusted
	// from schema_migrations and back-filled: a database migrated before this
	// table existed has no checksums to compare against.
	ToRecord []int
}

func (d historyDrift) empty() bool {
	return len(d.Mismatched) == 0
}

// classifyHistory compares the shipping files against the recorded history, up
// to the version golang-migrate reports as applied.
//
// Recorded versions with no file any more are deliberately not drift: that is
// the shape of a rollback to an older hub image, which stays safe because
// these migrations are additive. A hole in the *file* set is caught earlier,
// by checkMigrationSource.
func classifyHistory(files, recorded migrationSet, appliedThrough int) historyDrift {
	var d historyDrift
	for _, v := range files.versions() {
		if v > appliedThrough {
			continue // not applied yet; Up() will handle it on a later run
		}
		rec, ok := recorded[v]
		switch {
		case !ok:
			d.ToRecord = append(d.ToRecord, v)
		case rec != files[v]:
			d.Mismatched = append(d.Mismatched, v)
		}
	}
	return d
}

// verifyAndRecordHistory fails on drift between the recorded history and the
// shipping migrations, and records any applied version that has no row yet.
func verifyAndRecordHistory(ctx context.Context, pool *pgxpool.Pool, files migrationSet, appliedThrough int) error {
	if _, err := pool.Exec(ctx, schemaHistoryDDL); err != nil {
		return fmt.Errorf("create %s: %w", schemaHistoryTable, err)
	}
	recorded, err := loadHistory(ctx, pool)
	if err != nil {
		return err
	}
	drift := classifyHistory(files, recorded, appliedThrough)
	if !drift.empty() {
		return &HistoryDriftError{drift: drift, files: files, recorded: recorded}
	}
	for _, v := range drift.ToRecord {
		// ON CONFLICT DO NOTHING keeps concurrent hub startups harmless: the
		// first writer's checksum wins and every other writer agrees with it,
		// because they ship the same file.
		if _, err := pool.Exec(ctx,
			`INSERT INTO `+schemaHistoryTable+` (version, checksum) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`,
			v, files[v]); err != nil {
			return fmt.Errorf("record migration %d: %w", v, err)
		}
	}
	return nil
}

// checkMigrationSource verifies the embedded migration files are usable as a
// history: contiguous from 1, so that "version N applied" implies every lower
// version exists to be applied too.
func checkMigrationSource(files migrationSet) error {
	if missing := missingVersions(files); len(missing) > 0 {
		parts := make([]string, len(missing))
		for i, v := range missing {
			parts[i] = strconv.Itoa(v)
		}
		return fmt.Errorf("migration files have gaps at version %s: every version up to the highest must have a file", strings.Join(parts, ", "))
	}
	return nil
}

// loadHistory reads the recorded version -> checksum map. A missing table is
// not an error here: verifyAndRecordHistory creates it first, and a fresh
// database simply has no rows.
func loadHistory(ctx context.Context, pool *pgxpool.Pool) (migrationSet, error) {
	rows, err := pool.Query(ctx, `SELECT version, checksum FROM `+schemaHistoryTable)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", schemaHistoryTable, err)
	}
	defer rows.Close()
	set := migrationSet{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("scan %s: %w", schemaHistoryTable, err)
		}
		set[v] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", schemaHistoryTable, err)
	}
	return set, nil
}

// HistoryDriftError reports migrations that this database has applied from
// different file contents than the ones shipping now. It is fatal: the schema in
// this database does not match the code that is about to query it.
type HistoryDriftError struct {
	drift    historyDrift
	files    migrationSet
	recorded migrationSet
}

// Versions returns the migration versions whose recorded checksum differs from
// the file shipping now.
func (e *HistoryDriftError) Versions() []int { return e.drift.Mismatched }

func (e *HistoryDriftError) Error() string {
	var b strings.Builder
	b.WriteString("migration history drift: ")
	for i, v := range e.drift.Mismatched {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "applied version %d does not match migration file %d (recorded %s…, file %s…)",
			v, v, short(e.recorded[v]), short(e.files[v]))
	}
	fmt.Fprintf(&b, ". Migrations must never be renumbered or edited once deployed — "+
		"golang-migrate only tracks the highest applied version, so it will not fill the hole this leaves. "+
		"Reconcile the schema by hand, then align %s with what is actually applied (see docs/upgrade-guide.md).",
		schemaHistoryTable)
	return b.String()
}

func short(sum string) string {
	if len(sum) > 8 {
		return sum[:8]
	}
	return sum
}
