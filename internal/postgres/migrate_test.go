package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/paderinandrey/identity-service/db"
)

const (
	migrateTestDB  = "identity_migrate_test"
	migrateTestURL = "postgres://identity:identity@localhost:5433/" + migrateTestDB + "?sslmode=disable"
)

// TestMigration00007CollapsesOktaProviders seeds the pre-00007 layout —
// email-subject 'okta' links from the retired sign-in fallback next to
// 'okta-scim' links holding the stable id — and proves the data migration
// keeps exactly the stable ones under the single provider name.
func TestMigration00007CollapsesOktaProviders(t *testing.T) {
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, adminURL)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("PostgreSQL from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+migrateTestDB+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+migrateTestDB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+migrateTestDB+" WITH (FORCE)")
	})

	sqlDB, err := sql.Open("pgx", migrateTestURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	goose.SetBaseFS(db.Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, sqlDB, "migrations", 6); err != nil {
		t.Fatalf("migrate to 6: %v", err)
	}

	// Legacy layout: alice signed in via the email fallback and was also
	// provisioned; bob was only provisioned; carol only signed in.
	seed := `
		INSERT INTO users (id, email, name) VALUES
		  ('11111111-1111-1111-1111-111111111111', 'alice@example.com', 'Alice'),
		  ('22222222-2222-2222-2222-222222222222', 'bob@example.com', 'Bob'),
		  ('33333333-3333-3333-3333-333333333333', 'carol@example.com', 'Carol');
		INSERT INTO user_identities (user_id, provider, subject) VALUES
		  ('11111111-1111-1111-1111-111111111111', 'okta', 'alice@example.com'),
		  ('11111111-1111-1111-1111-111111111111', 'okta-scim', '00u-alice'),
		  ('22222222-2222-2222-2222-222222222222', 'okta-scim', '00u-bob'),
		  ('33333333-3333-3333-3333-333333333333', 'okta', 'carol@example.com');`
	if _, err := sqlDB.ExecContext(ctx, seed); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	if err := Migrate(ctx, migrateTestURL); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}

	rows, err := sqlDB.QueryContext(ctx,
		"SELECT user_id, provider, subject FROM user_identities ORDER BY subject")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var userID, provider, subject string
		if err := rows.Scan(&userID, &provider, &subject); err != nil {
			t.Fatal(err)
		}
		got[provider+"|"+subject] = userID
	}
	want := map[string]string{
		"okta|00u-alice": "11111111-1111-1111-1111-111111111111",
		"okta|00u-bob":   "22222222-2222-2222-2222-222222222222",
	}
	if len(got) != len(want) {
		t.Errorf("identities after migration = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("identity %s -> %q, want %q", k, got[k], v)
		}
	}

	var users int
	if err := sqlDB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil || users != 3 {
		t.Errorf("users after migration = %d, %v; want 3 untouched", users, err)
	}
}

// TestMigration00010BackfillsOutboxUserID seeds outbox rows recorded
// before user_id existed and proves the data migration lifts the user
// out of the payload for them.
func TestMigration00010BackfillsOutboxUserID(t *testing.T) {
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, adminURL)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("PostgreSQL from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(admin.Close)
	const dbName = "identity_migrate10_test"
	url := "postgres://identity:identity@localhost:5433/" + dbName + "?sslmode=disable"
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)") })

	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	goose.SetBaseFS(db.Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, sqlDB, "migrations", 8); err != nil {
		t.Fatalf("migrate to 8: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO users (id, email, name) VALUES ('44444444-4444-4444-4444-444444444444', 'old@example.com', 'Old');
		INSERT INTO user_events_outbox (event_type, payload) VALUES
		  ('identity.user.created', '{"user":{"id":"44444444-4444-4444-4444-444444444444"}}'::jsonb),
		  ('identity.user.snapshot', '{}'::jsonb);`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}
	var withUser, withoutUser int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE user_id = '44444444-4444-4444-4444-444444444444'),
		        count(*) FILTER (WHERE user_id IS NULL) FROM user_events_outbox`).Scan(&withUser, &withoutUser); err != nil {
		t.Fatal(err)
	}
	if withUser != 1 || withoutUser != 1 {
		t.Errorf("backfill: rows with user = %d, without = %d; want 1 and 1 (payload without a user stays NULL)", withUser, withoutUser)
	}
}
