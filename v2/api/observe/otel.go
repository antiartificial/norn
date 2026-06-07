package observe

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Shutdown func(context.Context) error

type Config struct {
	Enabled     bool
	ServiceName string
	EndpointURL string
	LogsEnabled bool
}

func ConfigFromEnv(service string) Config {
	enabled := envBool("NORN_OTEL_ENABLED") || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != ""
	return Config{
		Enabled:     enabled,
		ServiceName: envDefault("OTEL_SERVICE_NAME", service),
		EndpointURL: strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		LogsEnabled: enabled && envDefault("NORN_OTEL_LOGS", "true") != "false",
	}
}

func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "norn-api"
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)))
	if err != nil {
		return nil, err
	}
	var shutdowns []Shutdown
	traceOpts := []otlptracehttp.Option{}
	if cfg.EndpointURL != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(cfg.EndpointURL))
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(traceExporter))
	otel.SetTracerProvider(tracerProvider)
	shutdowns = append(shutdowns, tracerProvider.Shutdown)

	if cfg.LogsEnabled {
		logOpts := []otlploghttp.Option{}
		if cfg.EndpointURL != "" {
			logOpts = append(logOpts, otlploghttp.WithEndpointURL(cfg.EndpointURL))
		}
		logExporter, err := otlploghttp.New(ctx, logOpts...)
		if err != nil {
			return nil, err
		}
		loggerProvider := sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		)
		global.SetLoggerProvider(loggerProvider)
		shutdowns = append(shutdowns, loggerProvider.Shutdown)
	}

	return func(ctx context.Context) error {
		var joined error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			if err := shutdowns[i](ctx); err != nil {
				joined = errors.Join(joined, err)
			}
		}
		return joined
	}, nil
}

func ConfigureLogging(service string) {
	level := slog.LevelInfo
	format := strings.ToLower(strings.TrimSpace(envDefault("NORN_LOG_FORMAT", "text")))
	var stdout slog.Handler
	if format == "json" || format == "otel-json" {
		stdout = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		stdout = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	handler := stdout
	if envBool("NORN_OTEL_ENABLED") || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" {
		handler = fanoutHandler{handlers: []slog.Handler{stdout, otelslog.NewHandler(service)}}
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	log.SetOutput(slogWriter{logger: logger})
	log.SetFlags(0)
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
