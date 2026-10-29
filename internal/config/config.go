package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Значения по умолчанию.
const (
	defaultHTTPAddr      = ":8080"
	defaultMetricsAddr   = ":9091"
	defaultMaxUploadSize = 10 << 20 // 10 MiB
	defaultS3Region      = "us-east-1"

	defaultRateLimitRPS   = 20
	defaultRateLimitBurst = 40

	defaultOTLPEndpoint      = "jaeger:4317"
	defaultTracesSampleRatio = 1.0
	defaultEnvironment       = "local"
)

// Поддерживаемые брокеры сообщений.
const (
	BrokerRabbitMQ = "rabbitmq"
	BrokerKafka    = "kafka"
)

// Config — параметры запуска сервиса.
type Config struct {
	HTTPAddr      string
	LogLevel      slog.Level
	DatabaseURI   string
	MaxUploadSize int64

	S3            S3Config
	Broker        BrokerConfig
	RateLimit     RateLimitConfig
	Observability ObservabilityConfig

	// CORSAllowedOrigins — origins, которым браузер разрешает запросы к API
	// (scheme://host[:port] или "*"). Пусто — CORS выключен.
	CORSAllowedOrigins []string
}

// RateLimitConfig — ограничение частоты запросов с одного IP к API
// и веб-интерфейсу (token bucket).
type RateLimitConfig struct {
	// RPS — сколько запросов в секунду пополняется в «ведре». 0 — без ограничения.
	RPS float64
	// Burst — ёмкость «ведра»: сколько запросов можно сделать подряд.
	Burst int
}

// ObservabilityConfig — трейсы и метаданные сервиса в телеметрии.
type ObservabilityConfig struct {
	// OTLPEndpoint — куда слать трейсы по OTLP gRPC: host:port или URL.
	// Пусто — трейсы создаются (trace_id в логах и ответах есть), но никуда не уходят.
	OTLPEndpoint string
	// TracesSampleRatio — доля новых трейсов, которые записываются, от 0 до 1.
	// Решение вызывающего сервиса (родительский спан) важнее.
	TracesSampleRatio float64
	// Environment — deployment.environment в resource: local, staging, production.
	Environment string
	// MetricsAddr — адрес отдельного HTTP-сервера с /metrics. У server он не
	// совпадает с HTTP_ADDR: метрики (в них есть user_id) не должны быть видны клиентам API.
	MetricsAddr string
}

// S3Config — параметры запуска S3.
type S3Config struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// BrokerConfig — параметры запуска брокера (Kafka|RabbitMQ).
type BrokerConfig struct {
	Kind         string
	RabbitMQURL  string
	KafkaBrokers []string
}

// Ошибки конфигурации, при которых сервис не может быть запущен.
var (
	ErrDatabaseURINotFound  = errors.New("database URI not found")
	ErrS3ConfigNotFound     = errors.New("s3 config not found")
	ErrBrokerConfigNotFound = errors.New("broker config not found")
)

// Load собирает конфигурацию из переменных окружения.
func Load() (Config, error) {
	logLevel := slog.LevelInfo
	if v := getEnv("LOG_LEVEL", ""); v != "" {
		if err := logLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
		}
	}

	databaseURI := getEnv("DATABASE_URI", "")
	if databaseURI == "" {
		return Config{}, ErrDatabaseURINotFound
	}

	maxUploadSize := int64(defaultMaxUploadSize)
	if v := getEnv("MAX_UPLOAD_SIZE", ""); v != "" {
		var err error
		maxUploadSize, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("MAX_UPLOAD_SIZE: %w", err)
		}
	}

	s3, err := loadS3()
	if err != nil {
		return Config{}, err
	}

	broker, err := loadBroker()
	if err != nil {
		return Config{}, err
	}

	rateLimit, err := loadRateLimit()
	if err != nil {
		return Config{}, err
	}

	origins, err := loadCORSOrigins()
	if err != nil {
		return Config{}, err
	}

	observability, err := loadObservability()
	if err != nil {
		return Config{}, err
	}

	// Пустой Addr net/http трактует как ":http" (порт 80) — это почти наверняка
	// ошибка в окружении, а не намерение.
	httpAddr := getEnv("HTTP_ADDR", defaultHTTPAddr)
	if httpAddr == "" {
		return Config{}, errors.New("HTTP_ADDR: must not be empty")
	}

	return Config{
		HTTPAddr:           httpAddr,
		LogLevel:           logLevel,
		DatabaseURI:        databaseURI,
		MaxUploadSize:      maxUploadSize,
		S3:                 s3,
		Broker:             broker,
		RateLimit:          rateLimit,
		Observability:      observability,
		CORSAllowedOrigins: origins,
	}, nil
}

// loadCORSOrigins разбирает CORS_ALLOWED_ORIGINS: список через запятую.
// Origin сравнивается с заголовком браузера побайтно, поэтому путь, слеш
// в конце или опечатка в схеме сделали бы правило мёртвым — такие значения
// отклоняются при старте.
func loadCORSOrigins() ([]string, error) {
	var origins []string
	for _, o := range strings.Split(getEnv("CORS_ALLOWED_ORIGINS", ""), ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o != "*" {
			u, err := url.Parse(o)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
				u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS: %q is not an origin like https://example.com", o)
			}
		}
		origins = append(origins, o)
	}
	return origins, nil
}

// loadObservability читает переменные OpenTelemetry. Имена совпадают со
// стандартными OTEL_*, чтобы их можно было задавать так же, как для любого
// другого сервиса с OTel SDK.
func loadObservability() (ObservabilityConfig, error) {
	cfg := ObservabilityConfig{
		OTLPEndpoint:      getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", defaultOTLPEndpoint),
		TracesSampleRatio: defaultTracesSampleRatio,
		Environment:       getEnv("DEPLOYMENT_ENVIRONMENT", defaultEnvironment),
		MetricsAddr:       getEnv("METRICS_ADDR", defaultMetricsAddr),
	}
	if cfg.MetricsAddr == "" {
		return ObservabilityConfig{}, errors.New("METRICS_ADDR: must not be empty")
	}
	if v := getEnv("OTEL_TRACES_SAMPLER_ARG", ""); v != "" {
		ratio, err := strconv.ParseFloat(v, 64)
		if err != nil || ratio < 0 || ratio > 1 {
			return ObservabilityConfig{}, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG: want a number from 0 to 1, got %q", v)
		}
		cfg.TracesSampleRatio = ratio
	}
	return cfg, nil
}

func loadRateLimit() (RateLimitConfig, error) {
	cfg := RateLimitConfig{RPS: defaultRateLimitRPS, Burst: defaultRateLimitBurst}

	if v := getEnv("RATE_LIMIT_RPS", ""); v != "" {
		rps, err := strconv.ParseFloat(v, 64)
		if err != nil || rps < 0 {
			return RateLimitConfig{}, fmt.Errorf("RATE_LIMIT_RPS: want a non-negative number, got %q", v)
		}
		cfg.RPS = rps
	}
	if v := getEnv("RATE_LIMIT_BURST", ""); v != "" {
		burst, err := strconv.Atoi(v)
		if err != nil || burst < 1 {
			return RateLimitConfig{}, fmt.Errorf("RATE_LIMIT_BURST: want a positive integer, got %q", v)
		}
		cfg.Burst = burst
	}
	return cfg, nil
}

func loadS3() (S3Config, error) {
	cfg := S3Config{
		Endpoint:  getEnv("S3_ENDPOINT", ""),
		Region:    getEnv("S3_REGION", defaultS3Region),
		AccessKey: getEnv("S3_ACCESS_KEY", ""),
		SecretKey: getEnv("S3_SECRET_KEY", ""),
		Bucket:    getEnv("S3_BUCKET", ""),
	}

	if v := getEnv("S3_USE_SSL", ""); v != "" {
		useSSL, err := strconv.ParseBool(v)
		if err != nil {
			return S3Config{}, fmt.Errorf("S3_USE_SSL: %w", err)
		}
		cfg.UseSSL = useSSL
	}

	if cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Bucket == "" {
		return S3Config{}, fmt.Errorf("%w: S3_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY and S3_BUCKET are required", ErrS3ConfigNotFound)
	}

	return cfg, nil
}

func loadBroker() (BrokerConfig, error) {
	cfg := BrokerConfig{Kind: strings.ToLower(getEnv("BROKER", BrokerRabbitMQ))}

	switch cfg.Kind {
	case BrokerRabbitMQ:
		cfg.RabbitMQURL = getEnv("RABBITMQ_URL", "")
		if cfg.RabbitMQURL == "" {
			return BrokerConfig{}, fmt.Errorf("%w: RABBITMQ_URL is required", ErrBrokerConfigNotFound)
		}
	case BrokerKafka:
		for _, b := range strings.Split(getEnv("KAFKA_BROKERS", ""), ",") {
			if b = strings.TrimSpace(b); b != "" {
				cfg.KafkaBrokers = append(cfg.KafkaBrokers, b)
			}
		}
		if len(cfg.KafkaBrokers) == 0 {
			return BrokerConfig{}, fmt.Errorf("%w: KAFKA_BROKERS is required", ErrBrokerConfigNotFound)
		}
	default:
		return BrokerConfig{}, fmt.Errorf("BROKER: unsupported value %q, want %q or %q", cfg.Kind, BrokerRabbitMQ, BrokerKafka)
	}

	return cfg, nil
}

// getEnv возвращает значение переменной окружения или def, если она не задана.
// Явно заданная пустая строка возвращается как есть: решать, что она значит,
// должен вызывающий код, а не молча подменять её значением по умолчанию.
func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
