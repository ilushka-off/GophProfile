package config

import (
	"errors"
	"log/slog"
	"os"
	"reflect"
	"testing"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"DATABASE_URI":  "postgres://u:p@localhost:5432/db",
		"S3_ENDPOINT":   "localhost:8333",
		"S3_ACCESS_KEY": "key",
		"S3_SECRET_KEY": "secret",
		"S3_BUCKET":     "avatars",
		"RABBITMQ_URL":  "amqp://u:p@localhost:5672/",
	} {
		t.Setenv(k, v)
	}
	for _, k := range []string{
		"HTTP_ADDR", "LOG_LEVEL", "MAX_UPLOAD_SIZE", "S3_REGION", "S3_USE_SSL",
		"BROKER", "KAFKA_BROKERS", "RATE_LIMIT_RPS", "RATE_LIMIT_BURST", "CORS_ALLOWED_ORIGINS",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_TRACES_SAMPLER_ARG", "DEPLOYMENT_ENVIRONMENT",
	} {
		unsetEnv(t, k)
	}
}

// unsetEnv удаляет переменную на время теста; t.Setenv нужен, чтобы
// исходное значение восстановилось в Cleanup.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%s): %v", key, err)
	}
}

func TestLoadEmptyIsNotUnset(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("S3_REGION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.S3.Region != "" {
		t.Errorf("S3.Region = %q, want empty: explicitly empty value must not fall back to default", cfg.S3.Region)
	}
}

func TestLoadDefaults(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := Config{
		HTTPAddr:      defaultHTTPAddr,
		LogLevel:      slog.LevelInfo,
		DatabaseURI:   "postgres://u:p@localhost:5432/db",
		MaxUploadSize: defaultMaxUploadSize,
		S3: S3Config{
			Endpoint:  "localhost:8333",
			Region:    defaultS3Region,
			AccessKey: "key",
			SecretKey: "secret",
			Bucket:    "avatars",
		},
		Broker:    BrokerConfig{Kind: BrokerRabbitMQ, RabbitMQURL: "amqp://u:p@localhost:5672/"},
		RateLimit: RateLimitConfig{RPS: defaultRateLimitRPS, Burst: defaultRateLimitBurst},
		Observability: ObservabilityConfig{
			OTLPEndpoint:      defaultOTLPEndpoint,
			TracesSampleRatio: defaultTracesSampleRatio,
			Environment:       defaultEnvironment,
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("MAX_UPLOAD_SIZE", "1024")
	t.Setenv("S3_USE_SSL", "true")
	t.Setenv("BROKER", "kafka")
	t.Setenv("KAFKA_BROKERS", "k1:9092, k2:9092,")
	t.Setenv("RATE_LIMIT_RPS", "0.5")
	t.Setenv("RATE_LIMIT_BURST", "3")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example, http://localhost:3000,")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	t.Setenv("DEPLOYMENT_ENVIRONMENT", "staging")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.MaxUploadSize != 1024 {
		t.Errorf("MaxUploadSize = %d", cfg.MaxUploadSize)
	}
	if !cfg.S3.UseSSL {
		t.Error("S3.UseSSL = false")
	}
	if want := []string{"k1:9092", "k2:9092"}; !reflect.DeepEqual(cfg.Broker.KafkaBrokers, want) {
		t.Errorf("KafkaBrokers = %v, want %v", cfg.Broker.KafkaBrokers, want)
	}
	if cfg.RateLimit != (RateLimitConfig{RPS: 0.5, Burst: 3}) {
		t.Errorf("RateLimit = %+v", cfg.RateLimit)
	}
	if want := []string{"https://app.example", "http://localhost:3000"}; !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Errorf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, want)
	}
	if want := (ObservabilityConfig{TracesSampleRatio: 0.25, Environment: "staging"}); cfg.Observability != want {
		t.Errorf("Observability = %+v, want %+v", cfg.Observability, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr error
	}{
		{name: "no database uri", env: map[string]string{"DATABASE_URI": ""}, wantErr: ErrDatabaseURINotFound},
		{name: "bad log level", env: map[string]string{"LOG_LEVEL": "loud"}},
		{name: "bad max upload size", env: map[string]string{"MAX_UPLOAD_SIZE": "ten"}},
		{name: "no s3 bucket", env: map[string]string{"S3_BUCKET": ""}, wantErr: ErrS3ConfigNotFound},
		{name: "bad s3 use ssl", env: map[string]string{"S3_USE_SSL": "yes"}},
		{name: "empty http addr", env: map[string]string{"HTTP_ADDR": ""}},
		{name: "unknown broker", env: map[string]string{"BROKER": "nats"}},
		{name: "empty broker", env: map[string]string{"BROKER": ""}},
		{name: "no rabbitmq url", env: map[string]string{"RABBITMQ_URL": ""}, wantErr: ErrBrokerConfigNotFound},
		{name: "no kafka brokers", env: map[string]string{"BROKER": "kafka", "KAFKA_BROKERS": " , "}, wantErr: ErrBrokerConfigNotFound},
		{name: "bad rate limit", env: map[string]string{"RATE_LIMIT_RPS": "fast"}},
		{name: "negative rate limit", env: map[string]string{"RATE_LIMIT_RPS": "-1"}},
		{name: "zero burst", env: map[string]string{"RATE_LIMIT_BURST": "0"}},
		{name: "origin with path", env: map[string]string{"CORS_ALLOWED_ORIGINS": "https://app.example/"}},
		{name: "origin without scheme", env: map[string]string{"CORS_ALLOWED_ORIGINS": "app.example"}},
		{name: "bad sampler arg", env: map[string]string{"OTEL_TRACES_SAMPLER_ARG": "all"}},
		{name: "sampler arg above one", env: map[string]string{"OTEL_TRACES_SAMPLER_ARG": "1.5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBaseEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("Load() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
