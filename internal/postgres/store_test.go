package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/xometry-europe-gmbh/identity-service/db"
	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

const (
	adminURL = "postgres://identity:identity@localhost:5433/identity_development?sslmode=disable"
	testDB   = "identity_store_test"
	testURL  = "postgres://identity:identity@localhost:5433/" + testDB + "?sslmode=disable"
)

// newTestStore provisions a dedicated migrated database per test run.
// Tests are skipped when the docker-compose PostgreSQL is not running.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, adminURL)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("PostgreSQL from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(admin.Close)

	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+testDB); err != nil {
		t.Fatalf("drop test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+testDB); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+testDB+" WITH (FORCE)")
	})

	if err := Migrate(ctx, testURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool)
}

func mustCreateUser(t *testing.T, store *Store, email, name string) *identity.User {
	t.Helper()
	user, err := store.UpsertByEmail(t.Context(), email, name)
	if err != nil {
		t.Fatalf("UpsertByEmail(%q): %v", email, err)
	}
	return user
}

func TestStore(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	t.Run("upsert is idempotent and email is case-insensitive unique", func(t *testing.T) {
		first := mustCreateUser(t, store, "Case@Example.com", "First")
		second := mustCreateUser(t, store, "case@example.COM", "Renamed")
		if first.ID != second.ID {
			t.Errorf("emails differing only by case created two users: %s / %s", first.ID, second.ID)
		}
		if second.Name != "Renamed" {
			t.Errorf("upsert must update name, got %q", second.Name)
		}

		var raw string
		err := store.pool.QueryRow(ctx,
			"INSERT INTO users (email) VALUES ('CASE@example.com') RETURNING id").Scan(&raw)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Errorf("direct duplicate insert: err = %v, want unique_violation", err)
		}
	})

	t.Run("uuid stays stable when email changes", func(t *testing.T) {
		u := mustCreateUser(t, store, "stable@example.com", "Stable")
		if _, err := store.pool.Exec(ctx,
			"UPDATE users SET email = 'renamed@example.com' WHERE id = $1", u.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.FindByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != u.ID || got.Email != "renamed@example.com" {
			t.Errorf("FindByID after email change = %+v", got)
		}
	})

	t.Run("identities: attach, find, uniqueness", func(t *testing.T) {
		u := mustCreateUser(t, store, "ident@example.com", "Ident")

		if err := store.AttachIdentity(ctx, u.ID, identity.ProviderOkta, "subj-1"); err != nil {
			t.Fatalf("AttachIdentity: %v", err)
		}
		got, err := store.FindByIdentity(ctx, identity.ProviderOkta, "subj-1")
		if err != nil || got.ID != u.ID {
			t.Fatalf("FindByIdentity = %v, %v; want user %s", got, err, u.ID)
		}

		linked, err := store.HasIdentity(ctx, u.ID, identity.ProviderOkta)
		if err != nil || !linked {
			t.Errorf("HasIdentity = %v, %v; want true", linked, err)
		}

		other := mustCreateUser(t, store, "other@example.com", "Other")
		if err := store.AttachIdentity(ctx, other.ID, identity.ProviderOkta, "subj-1"); err == nil {
			t.Error("duplicate (provider, subject) must be rejected")
		}
		if err := store.AttachIdentity(ctx, u.ID, identity.ProviderOkta, "subj-2"); err == nil {
			t.Error("second identity of the same provider for one user must be rejected")
		}
	})

	t.Run("find active by email skips inactive", func(t *testing.T) {
		u := mustCreateUser(t, store, "inactive@example.com", "Inactive")
		if _, err := store.pool.Exec(ctx,
			"UPDATE users SET active = false WHERE id = $1", u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FindActiveByEmail(ctx, "inactive@example.com"); !errors.Is(err, identity.ErrUserNotFound) {
			t.Errorf("FindActiveByEmail for inactive user: err = %v, want ErrUserNotFound", err)
		}
	})

	t.Run("touch last sign in", func(t *testing.T) {
		u := mustCreateUser(t, store, "touch@example.com", "Touch")
		if u.LastSignInAt != nil {
			t.Fatal("fresh user must have no last_sign_in_at")
		}
		if err := store.TouchLastSignIn(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.FindByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastSignInAt == nil || time.Since(*got.LastSignInAt) > time.Minute {
			t.Errorf("LastSignInAt = %v, want recent", got.LastSignInAt)
		}
	})
}

func TestMigrationsRollback(t *testing.T) {
	newTestStore(t) // provisions and migrates the test database

	sqlDB, err := sql.Open("pgx", testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(db.Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(t.Context(), sqlDB, "migrations", 0); err != nil {
		t.Fatalf("goose down: %v", err)
	}

	var exists bool
	if err := sqlDB.QueryRowContext(t.Context(),
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("users table must be gone after full rollback")
	}

	if err := goose.UpContext(t.Context(), sqlDB, "migrations"); err != nil {
		t.Fatalf("goose up after rollback: %v", err)
	}
}

func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "postgres integration tests use docker-compose database on localhost:5433")
	os.Exit(m.Run())
}
