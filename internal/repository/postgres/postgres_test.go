package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const postgresImage = "postgres:17-alpine"

// RepositorySuite — интеграционные тесты репозиториев на настоящем PostgreSQL
// в контейнере. Контейнер поднимается один раз на весь набор, миграции
// применяются при старте, перед каждым тестом таблицы очищаются.
type RepositorySuite struct {
	suite.Suite

	container   *tcpostgres.PostgresContainer
	databaseURI string
	pool        *pgxpool.Pool
	repo        *AvatarRepository
}

// TestRepositorySuite пропускается с -short и без Docker.
func TestRepositorySuite(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped with -short")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	suite.Run(t, new(RepositorySuite))
}

func (s *RepositorySuite) SetupSuite() {
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("gophprofile"),
		tcpostgres.WithPassword("gophprofile"),
		tcpostgres.BasicWaitStrategies(),
	)
	s.container = ctr
	s.Require().NoError(err, "start postgres")

	s.databaseURI, err = ctr.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)
	s.Require().NoError(RunMigrations(s.databaseURI), "run migrations")

	s.pool, err = OpenPool(ctx, s.databaseURI)
	s.Require().NoError(err, "open pool")
	s.repo = NewAvatarRepository(s.pool)
}

func (s *RepositorySuite) TearDownSuite() {
	if s.pool != nil {
		s.pool.Close()
	}
	if s.container != nil {
		s.NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *RepositorySuite) SetupTest() {
	_, err := s.pool.Exec(context.Background(), `TRUNCATE avatars, processed_messages`)
	s.Require().NoError(err, "truncate tables")
}

func (s *RepositorySuite) TestRunMigrationsIsIdempotent() {
	s.NoError(RunMigrations(s.databaseURI), "second run")
}

func TestOpenPool(t *testing.T) {
	t.Run("malformed uri", func(t *testing.T) {
		if _, err := OpenPool(context.Background(), "://nonsense"); err == nil {
			t.Fatal("expected error for a malformed database uri")
		}
	})

	t.Run("unreachable database", func(t *testing.T) {
		_, err := OpenPool(context.Background(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
		if err == nil {
			t.Fatal("expected error for an unreachable database")
		}
	})
}

func TestRunMigrationsMalformedURI(t *testing.T) {
	if err := RunMigrations("://nonsense"); err == nil {
		t.Fatal("expected error for a malformed database uri")
	}
}
