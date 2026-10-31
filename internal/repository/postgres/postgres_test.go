package postgres

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
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

// Запросы внутри трейса становятся дочерними спанами с атрибутами БД
// и текстом запроса без параметров; вне трейса спанов нет.
func (s *RepositorySuite) TestQueriesAreTraced() {
	ctx := context.Background()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	defer func() { _ = tp.Shutdown(ctx) }()

	pool, err := openPool(ctx, s.databaseURI, otelpgx.NewTracer(
		otelpgx.WithTracerProvider(tp), otelpgx.WithDisableAcquireTracer()))
	s.Require().NoError(err)
	defer pool.Close()
	repo := NewAvatarRepository(pool)

	// Вне трейса.
	_, err = repo.ListByUser(ctx, "user-1")
	s.Require().NoError(err)
	s.Empty(rec.Ended(), "query outside a trace must not start a root span")

	parentCtx, parent := tp.Tracer("test").Start(ctx, "GET /api/v1/users/{user_id}/avatars")
	_, err = repo.ListByUser(parentCtx, "user-1")
	s.Require().NoError(err)
	parent.End()

	var query sdktrace.ReadOnlySpan
	for _, span := range rec.Ended() {
		if span.SpanContext().SpanID() != parent.SpanContext().SpanID() {
			query = span
		}
	}
	s.Require().NotNil(query, "no query span")
	s.Equal(parent.SpanContext().SpanID(), query.Parent().SpanID(), "query span is not a child of the request span")
	s.Equal("SELECT", query.Name())

	attrs := map[attribute.Key]string{}
	for _, kv := range query.Attributes() {
		attrs[kv.Key] = kv.Value.String()
	}
	s.Equal("postgresql", attrs[semconv.DBSystemNameKey])
	s.Equal("SELECT", attrs[semconv.DBOperationNameKey])
	s.Contains(attrs[semconv.DBQueryTextKey], "WHERE user_id = $1")
	s.NotContains(attrs[semconv.DBQueryTextKey], "user-1", "query parameters must not be recorded")
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

// Недоступная база не роняет сбор метрик: серии просто пропускаются.
func TestStorageCollectorDatabaseDown(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	c := NewStorageCollector(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Errorf("collected %d series with the database down, want 0", n)
	}
}
