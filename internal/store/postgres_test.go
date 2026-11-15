package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/JiffyCRM/identity-link/internal/hash"
	"github.com/JiffyCRM/identity-link/internal/migrate"
	"github.com/JiffyCRM/identity-link/internal/store"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var dsn string

	if envDSN := os.Getenv("TEST_DATABASE_URL"); envDSN != "" {
		dsn = envDSN
	} else {
		container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("identity_link"),
			tcpostgres.WithUsername("identity_link"),
			tcpostgres.WithPassword("identity_link"),
		)
		if err != nil {
			t.Skipf("Docker is not available for integration tests: %v", err)
		}
		t.Cleanup(func() {
			_ = container.Terminate(context.Background())
		})

		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("get container connection string: %v", err)
		}
	}

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database config: %v", err)
	}
	poolConfig.MaxConns = 25

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create database pool: %v", err)
	}
	t.Cleanup(pool.Close)

	deadline := time.Now().Add(45 * time.Second)
	for {
		pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
		err = pool.Ping(pingCtx)
		pingCancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("database did not become ready: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}

	if _, err := migrate.RunUp(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return pool
}

func newHasher(t *testing.T) *hash.Hasher {
	t.Helper()
	hasher, err := hash.New([]byte(testSecret))
	if err != nil {
		t.Fatalf("hash.New() error = %v", err)
	}
	return hasher
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE user_emails, provider_accounts, users"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}

func TestPostgresStore(t *testing.T) {
	pool := setupTestDB(t)
	st := store.NewPostgres(pool)
	hasher := newHasher(t)
	ctx := context.Background()

	t.Run("get or create", func(t *testing.T) {
		truncate(t, pool)

		user, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash(" User@Example.com "))
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}
		if user.ID == uuid.Nil {
			t.Fatal("GetOrCreate() returned a nil user id")
		}
		countRows(t, pool, "users", 1)
		countRows(t, pool, "user_emails", 1)
		countRows(t, pool, "provider_accounts", 1)

		var storedHash []byte
		if err := pool.QueryRow(ctx, "SELECT email_hash FROM user_emails").Scan(&storedHash); err != nil {
			t.Fatalf("load stored hash: %v", err)
		}
		if !bytes.Equal(storedHash, hasher.Hash("user@example.com")) {
			t.Fatal("stored email hash does not match the normalized email hash")
		}

		again, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash("user@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() repeat error = %v", err)
		}
		if again.ID != user.ID {
			t.Fatalf("repeat GetOrCreate() user = %s, want %s", again.ID, user.ID)
		}
		countRows(t, pool, "provider_accounts", 1)

		linked, err := st.GetOrCreate(ctx, "github", "github-subject", hasher.Hash("user@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() with new provider error = %v", err)
		}
		if linked.ID != user.ID {
			t.Fatalf("email-linked user = %s, want %s", linked.ID, user.ID)
		}
		countRows(t, pool, "users", 1)
		countRows(t, pool, "provider_accounts", 2)

		withDifferentEmail, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash("different@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() with different email error = %v", err)
		}
		if withDifferentEmail.ID != user.ID {
			t.Fatalf("provider lookup did not win over email lookup: got %s, want %s", withDifferentEmail.ID, user.ID)
		}
		countRows(t, pool, "user_emails", 1)
	})

	t.Run("find by email", func(t *testing.T) {
		truncate(t, pool)

		user, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash("found@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}

		found, err := st.FindByEmailHash(ctx, hasher.Hash(" FOUND@example.com "))
		if err != nil {
			t.Fatalf("FindByEmailHash() error = %v", err)
		}
		if found.ID != user.ID {
			t.Fatalf("FindByEmailHash() user = %s, want %s", found.ID, user.ID)
		}

		if _, err := st.FindByEmailHash(ctx, hasher.Hash("missing@example.com")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("FindByEmailHash() unknown email error = %v, want ErrNotFound", err)
		}
	})

	t.Run("lookup by provider and user id", func(t *testing.T) {
		truncate(t, pool)

		user, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash("lookup@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}

		byProvider, err := st.GetByProvider(ctx, "google", "google-subject")
		if err != nil {
			t.Fatalf("GetByProvider() error = %v", err)
		}
		if byProvider.ID != user.ID {
			t.Fatalf("GetByProvider() user = %s, want %s", byProvider.ID, user.ID)
		}

		byID, err := st.GetByUserID(ctx, user.ID)
		if err != nil {
			t.Fatalf("GetByUserID() error = %v", err)
		}
		if byID.ID != user.ID {
			t.Fatalf("GetByUserID() user = %s, want %s", byID.ID, user.ID)
		}

		if _, err := st.GetByProvider(ctx, "google", "unknown-subject"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetByProvider() unknown error = %v, want ErrNotFound", err)
		}
		if _, err := st.GetByUserID(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetByUserID() unknown error = %v, want ErrNotFound", err)
		}
	})

	t.Run("link and unlink provider", func(t *testing.T) {
		truncate(t, pool)

		user, err := st.GetOrCreate(ctx, "google", "google-subject", hasher.Hash("link@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}

		linked, err := st.LinkProvider(ctx, user.ID, "github", "github-subject", hasher.Hash("link@example.com"))
		if err != nil {
			t.Fatalf("LinkProvider() error = %v", err)
		}
		if linked.ID != user.ID {
			t.Fatalf("LinkProvider() user = %s, want %s", linked.ID, user.ID)
		}

		providers, err := st.ListProviders(ctx, user.ID)
		if err != nil {
			t.Fatalf("ListProviders() error = %v", err)
		}
		if len(providers) != 2 {
			t.Fatalf("ListProviders() count = %d, want 2", len(providers))
		}

		if _, err := st.LinkProvider(ctx, user.ID, "github", "github-subject", hasher.Hash("link@example.com")); err != nil {
			t.Fatalf("LinkProvider() repeat error = %v", err)
		}
		countRows(t, pool, "provider_accounts", 2)

		if _, err := st.LinkProvider(ctx, user.ID, "github", "github-subject", hasher.Hash("other@example.com")); !errors.Is(err, store.ErrEmailMismatch) {
			t.Fatalf("LinkProvider() wrong email error = %v, want ErrEmailMismatch", err)
		}

		if _, err := st.LinkProvider(ctx, uuid.New(), "github", "another-subject", hasher.Hash("link@example.com")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("LinkProvider() unknown user error = %v, want ErrNotFound", err)
		}

		other, err := st.GetOrCreate(ctx, "github", "other-subject", hasher.Hash("other@example.com"))
		if err != nil {
			t.Fatalf("GetOrCreate() second user error = %v", err)
		}
		if _, err := st.LinkProvider(ctx, user.ID, "github", "other-subject", hasher.Hash("link@example.com")); !errors.Is(err, store.ErrAlreadyLinked) {
			t.Fatalf("LinkProvider() already linked error = %v, want ErrAlreadyLinked", err)
		}
		if _, err := st.GetByProvider(ctx, "github", "other-subject"); err != nil {
			t.Fatalf("GetByProvider() second user error = %v", err)
		}

		if err := st.UnlinkProvider(ctx, user.ID, "github", "github-subject"); err != nil {
			t.Fatalf("UnlinkProvider() error = %v", err)
		}
		if err := st.UnlinkProvider(ctx, user.ID, "github", "github-subject"); !errors.Is(err, store.ErrProviderNotLinked) {
			t.Fatalf("UnlinkProvider() repeat error = %v, want ErrProviderNotLinked", err)
		}

		providers, err = st.ListProviders(ctx, user.ID)
		if err != nil {
			t.Fatalf("ListProviders() after unlink error = %v", err)
		}
		if len(providers) != 1 {
			t.Fatalf("ListProviders() after unlink count = %d, want 1", len(providers))
		}

		if err := st.UnlinkProvider(ctx, user.ID, "google", "google-subject"); err != nil {
			t.Fatalf("UnlinkProvider() last provider error = %v", err)
		}
		if _, err := st.GetByProvider(ctx, "google", "google-subject"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetByProvider() after unlink error = %v, want ErrNotFound", err)
		}

		empty, err := st.ListProviders(ctx, other.ID)
		if err != nil {
			t.Fatalf("ListProviders() other user error = %v", err)
		}
		if len(empty) != 1 {
			t.Fatalf("ListProviders() other user count = %d, want 1", len(empty))
		}

		if _, err := st.ListProviders(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ListProviders() unknown user error = %v, want ErrNotFound", err)
		}
	})

	t.Run("concurrent same identity", func(t *testing.T) {
		truncate(t, pool)

		const goroutines = 20
		emailHash := hasher.Hash("race@example.com")
		ids := make([]uuid.UUID, goroutines)
		errs := make([]error, goroutines)

		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				user, err := st.GetOrCreate(context.Background(), "google", "race-subject", emailHash)
				ids[index] = user.ID
				errs[index] = err
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("goroutine %d: GetOrCreate() error = %v", i, err)
			}
		}
		for i := 1; i < goroutines; i++ {
			if ids[i] != ids[0] {
				t.Fatalf("goroutine %d returned user %s, want %s", i, ids[i], ids[0])
			}
		}

		countRows(t, pool, "users", 1)
		countRows(t, pool, "user_emails", 1)
		countRows(t, pool, "provider_accounts", 1)
	})

	t.Run("concurrent same email different providers", func(t *testing.T) {
		truncate(t, pool)

		const goroutines = 20
		emailHash := hasher.Hash("race-two@example.com")
		ids := make([]uuid.UUID, goroutines)
		errs := make([]error, goroutines)

		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				user, err := st.GetOrCreate(
					context.Background(),
					fmt.Sprintf("provider-%d", index),
					fmt.Sprintf("subject-%d", index),
					emailHash,
				)
				ids[index] = user.ID
				errs[index] = err
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("goroutine %d: GetOrCreate() error = %v", i, err)
			}
		}
		for i := 1; i < goroutines; i++ {
			if ids[i] != ids[0] {
				t.Fatalf("goroutine %d returned user %s, want %s", i, ids[i], ids[0])
			}
		}

		countRows(t, pool, "users", 1)
		countRows(t, pool, "user_emails", 1)
		countRows(t, pool, "provider_accounts", goroutines)
	})
}
