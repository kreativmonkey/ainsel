package usertokens_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ainsel-project/ainsel/services/hub/internal/db"
	"github.com/ainsel-project/ainsel/services/hub/internal/usertokens"
)

func newTestStore(t *testing.T) (*usertokens.Store, func()) {
	t.Helper()
	store, _, cleanup := newTestStoreWithPool(t)
	return store, cleanup
}

// newTestStoreWithPool is newTestStore with direct pool access, for tests
// that must mutate rows outside the usertokens API (e.g. toggling the
// users.disabled flag).
func newTestStoreWithPool(t *testing.T) (*usertokens.Store, *pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	c, err := pgcontainer.Run(ctx, "postgres:17-alpine",
		pgcontainer.WithDatabase("ainsel_test"),
		pgcontainer.WithUsername("test"),
		pgcontainer.WithPassword("test"),
		pgcontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("open pool: %v", err)
	}

	// Insert test users referenced by user_tokens.user_id FK.
	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, email, username) VALUES ('user1', 'u1@test.com', 'user1'),
		                                               ('user2', 'u2@test.com', 'user2')
	`)
	if err != nil {
		pool.Close()
		_ = c.Terminate(ctx)
		t.Fatalf("seed users: %v", err)
	}

	cleanup := func() {
		pool.Close()
		_ = c.Terminate(context.Background())
	}
	return usertokens.NewStore(pool), pool, cleanup
}
