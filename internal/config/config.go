package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Значения по умолчанию.
const (
	defaultHTTPAddr      = ":8080"
	defaultMaxUploadSize = 10 << 20 // 10 MiB
	maxMaxUploadSize     = 10 << 20 // 10 MiB
	defaultS3Region      = "us-east-1"

	defaultRateLimitRPS   = 20
	defaultRateLimitBurst = 40
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

	S3        S3Config
	Broker    BrokerConfig
	RateLimit RateLimitConfig
}

// RateLimitConfig — ограничение частоты запросов с одного IP к API
// и веб-интерфейсу (token bucket).
type RateLimitConfig struct {
	// RPS — сколько запросов в секунду пополняется в «ведре». 0 — без ограничения.
	RPS float64
	// Burst — ёмкость «ведра»: сколько запросов можно сделать подряд.
	Burst int
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
	if maxUploadSize < 1 || maxUploadSize > maxMaxUploadSize {
		return Config{}, fmt.Errorf("MAX_UPLOAD_SIZE: must be between 1 and %d bytes, got %d", maxMaxUploadSize, maxUploadSize)
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

	return Config{
		HTTPAddr:      getEnv("HTTP_ADDR", defaultHTTPAddr),
		LogLevel:      logLevel,
		DatabaseURI:   databaseURI,
		MaxUploadSize: maxUploadSize,
		S3:            s3,
		Broker:        broker,
		RateLimit:     rateLimit,
	}, nil
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

// getEnv возвращает значение переменной окружения или def, если она не задана или пуста.
func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
