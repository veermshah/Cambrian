package db

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Run applies all pending up migrations against directURL. It MUST be called
// with the direct (non-pooled) Postgres URL — golang-migrate uses advisory
// locks and prepared statements that Supabase's pgBouncer transaction-pooling
// endpoint does not support.
//
// Run is idempotent: calling it twice is a no-op once the schema is current.
func Run(directURL string) error {
	if directURL == "" {
		return errors.New("db.Run: DATABASE_URL is empty")
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db.Run: open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5://"+stripScheme(directURL))
	if err != nil {
		return fmt.Errorf("db.Run: init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("db.Run: apply up migrations: %w", err)
	}
	return nil
}

// Down rolls every migration back. Used by tests and the operator who needs
// to reset a devnet database; never called from cmd/swarm.
func Down(directURL string) error {
	if directURL == "" {
		return errors.New("db.Down: DATABASE_URL is empty")
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db.Down: open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5://"+stripScheme(directURL))
	if err != nil {
		return fmt.Errorf("db.Down: init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("db.Down: apply down migrations: %w", err)
	}
	return nil
}

// stripScheme drops a leading postgres:// or postgresql:// so the result can
// be reattached behind the pgx5:// scheme that golang-migrate's pgx/v5
// driver expects.
func stripScheme(url string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return url[len(prefix):]
		}
	}
	return url
}
