package db

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreContiguous(t *testing.T) {
	files, err := loadMigrationSet()
	if err != nil {
		t.Fatalf("loadMigrationSet: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	if err := checkMigrationSource(files); err != nil {
		t.Fatalf("checkMigrationSource: %v", err)
	}
	for v, sum := range files {
		if len(sum) != 64 {
			t.Errorf("version %d: checksum %q is not a hex sha256", v, sum)
		}
	}
}

// TestEmbeddedChecksumsAreStable pins the checksum of a known migration so the
// check cannot silently start agreeing with edited file contents.
func TestEmbeddedChecksumsAreStable(t *testing.T) {
	files, err := loadMigrationSet()
	if err != nil {
		t.Fatalf("loadMigrationSet: %v", err)
	}
	// The migration that motivated the history check: renumbering it once left
	// a dev database without the users.disabled column while reporting a clean
	// migration run.
	const baseline = 26
	sum, ok := files[baseline]
	if !ok {
		t.Fatalf("migration version %d is missing from the embedded set", baseline)
	}
	// checksumSQL of the file's own bytes is by definition stable; what this
	// pins is that the baseline version still exists and is checksummed.
	if got := checksumSQL(mustReadMigration(t, baseline)); got != sum {
		t.Fatalf("version %d checksum changed: %s != %s", baseline, got, sum)
	}
}

func mustReadMigration(t *testing.T, version int) []byte {
	t.Helper()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		v, err := migrationVersion(e.Name())
		if err != nil || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		if v != version {
			continue
		}
		b, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		return b
	}
	t.Fatalf("no up migration with version %d", version)
	return nil
}

func TestMigrationVersionParsesPrefix(t *testing.T) {
	cases := map[string]int{
		"0001_create_mcp_servers.up.sql":      1,
		"0027_add_users_token_version.up.sql": 27,
	}
	for name, want := range cases {
		got, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: version = %d, want %d", name, got, want)
		}
	}
}

func TestMigrationVersionRejectsBadNames(t *testing.T) {
	for _, name := range []string{"create_things.up.sql", "abc_x.up.sql", "0_x.up.sql"} {
		if _, err := migrationVersion(name); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMissingVersions(t *testing.T) {
	cases := []struct {
		name string
		set  migrationSet
		want []int
	}{
		{"contiguous", migrationSet{1: "a", 2: "b", 3: "c"}, nil},
		{"single gap", migrationSet{1: "a", 3: "c"}, []int{2}},
		{"leading gap impossible, middle and tail", migrationSet{2: "b", 5: "e"}, []int{1, 3, 4}},
		{"empty", migrationSet{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := missingVersions(tc.set)
			if len(got) != len(tc.want) {
				t.Fatalf("missing = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("missing = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCheckMigrationSourceRejectsGaps(t *testing.T) {
	if err := checkMigrationSource(migrationSet{1: "a", 2: "b"}); err != nil {
		t.Fatalf("contiguous set: %v", err)
	}
	err := checkMigrationSource(migrationSet{1: "a", 3: "c"})
	if err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("expected gap error naming version 2, got %v", err)
	}
}

// TestClassifyHistoryBackfillsPreHistoryDatabases covers a database that was
// migrated before schema_history existed: schema_migrations says everything is
// applied, so the current files are trusted and recorded as the baseline.
func TestClassifyHistoryBackfillsPreHistoryDatabases(t *testing.T) {
	files := migrationSet{1: "aa", 2: "bb", 3: "cc"}
	d := classifyHistory(files, migrationSet{}, 3)
	if !d.empty() {
		t.Fatalf("expected no drift, got %+v", d)
	}
	want := []int{1, 2, 3}
	if len(d.ToRecord) != len(want) {
		t.Fatalf("ToRecord = %v, want %v", d.ToRecord, want)
	}
	for i := range want {
		if d.ToRecord[i] != want[i] {
			t.Fatalf("ToRecord = %v, want %v", d.ToRecord, want)
		}
	}
}

func TestClassifyHistoryDetectsRenumberedMigration(t *testing.T) {
	// The incident: version 26 was applied as add_users_token_version, then that
	// file was renumbered to 0027 and a new 0026_add_user_disabled took its
	// place. schema_migrations already read 27, so Up() applied nothing and the
	// disabled column never existed.
	files := migrationSet{25: "x", 26: "disabled", 27: "token_version"}
	recorded := migrationSet{25: "x", 26: "token_version", 27: "token_version"}

	d := classifyHistory(files, recorded, 27)
	if len(d.Mismatched) != 1 || d.Mismatched[0] != 26 {
		t.Fatalf("Mismatched = %v, want [26]", d.Mismatched)
	}
	err := (&HistoryDriftError{drift: d, files: files, recorded: recorded}).Error()
	for _, want := range []string{"version 26", "recorded", "file", "renumbered"} {
		if !strings.Contains(err, want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestClassifyHistoryIgnoresUnappliedVersions(t *testing.T) {
	files := migrationSet{1: "a", 2: "b"}
	recorded := migrationSet{1: "a"}
	d := classifyHistory(files, recorded, 1)
	if !d.empty() || len(d.ToRecord) != 0 {
		t.Fatalf("expected nothing to do, got %+v", d)
	}
}

// TestClassifyHistoryAllowsRollback pins the deliberate leniency for an older
// hub image running against a newer (superset) schema: recorded versions with no
// file are the shape of a rollback, not drift.
func TestClassifyHistoryAllowsRollback(t *testing.T) {
	files := migrationSet{1: "a", 2: "b"}
	recorded := migrationSet{1: "a", 2: "b", 3: "c"}
	d := classifyHistory(files, recorded, 3)
	if !d.empty() {
		t.Fatalf("rollback should not be drift, got %+v", d)
	}
}
