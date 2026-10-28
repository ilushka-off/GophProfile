package postgres

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// errInvalidDatabaseURI возвращается вместо ошибки url.Parse:
// та содержит исходную строку целиком, вместе с паролем.
var errInvalidDatabaseURI = errors.New("invalid database URI")

// toPgxMigrateURI меняет схему postgres:// или postgresql:// на pgx5://,
// которую понимает драйвер golang-migrate для pgx/v5.
func toPgxMigrateURI(databaseURI string) (string, error) {
	u, err := url.Parse(databaseURI)
	if err != nil {
		return "", errInvalidDatabaseURI
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		u.Scheme = "pgx5"
		return u.String(), nil
	default:
		return "", fmt.Errorf("unsupported database uri scheme: %s", u.Scheme)
	}
}

// RunMigrations применяет все новые миграции из встроенных SQL-файлов.
// databaseURI — строка вида postgres://user:pass@host:port/db.
// Если применять нечего, это не ошибка.
func RunMigrations(databaseURI string) error {
	sourceDriver, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("create migrations source: %w", err)
	}

	migrationURI, err := toPgxMigrateURI(databaseURI)
	if err != nil {
		return fmt.Errorf("build migration uri: %w", err)
	}

	migrator, err := migrate.NewWithSourceInstance("iofs", sourceDriver, migrationURI)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}

	defer func() {
		srcErr, dbErr := migrator.Close()
		if closeErr := errors.Join(srcErr, dbErr); closeErr != nil {
			slog.Warn("close migrator", "err", closeErr)
		}
	}()

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
