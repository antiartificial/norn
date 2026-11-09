package observe

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path"
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
	Enabled           bool
	TracesEnabled     bool
	ServiceName       string
	EndpointURL       string
	TracesEndpointURL string
	LogsEndpointURL   string
	LogsEnabled       bool
}

func ConfigFromEnv(service string) Config {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	tracesEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	logsEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"))
	tracesEnabled := envBool("NORN_OTEL_ENABLED") || endpoint != "" || tracesEndpoint != ""
	logsEnabled := envBool("NORN_OTEL_LOGS") && (envBool("NORN_OTEL_ENABLED") || endpoint != "" || logsEndpoint != "")
	return Config{
		Enabled:           tracesEnabled || logsEnabled,
		TracesEnabled:     tracesEnabled,
		ServiceName:       envDefault("OTEL_SERVICE_NAME", service),
		EndpointURL:       endpoint,
		TracesEndpointURL: tracesEndpoint,
		LogsEndpointURL:   logsEndpoint,
		LogsEnabled:       logsEnabled,
	}
}

func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "norn-api"
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)))
	if err != nil {
		return nil, err
	}
	var shutdowns []Shutdown
	if cfg.TracesEnabled {
		traceOpts := []otlptracehttp.Option{}
		traceEndpoint, err := signalEndpointURL(cfg.EndpointURL, cfg.TracesEndpointURL, "v1/traces")
		if err != nil {
			return nil, err
		}
		if traceEndpoint != "" {
			traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(traceEndpoint))
		}
		traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
		if err != nil {
			return nil, err
		}
		tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(traceExporter))
		otel.SetTracerProvider(tracerProvider)
		shutdowns = append(shutdowns, tracerProvider.Shutdown)
	}

	if cfg.LogsEnabled {
		logOpts := []otlploghttp.Option{}
		logsEndpoint, err := signalEndpointURL(cfg.EndpointURL, cfg.LogsEndpointURL, "v1/logs")
		if err != nil {
			return nil, err
		}
		if logsEndpoint != "" {
			logOpts = append(logOpts, otlploghttp.WithEndpointURL(logsEndpoint))
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

// signalEndpointURL follows the OTLP HTTP endpoint rules: a signal-specific
// endpoint is already complete, while the generic endpoint is a base URL to
// which the signal path must be appended.
func signalEndpointURL(base, signalSpecific, signalPath string) (string, error) {
	if endpoint := strings.TrimSpace(signalSpecific); endpoint != "" {
		return endpoint, nil
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid OTLP endpoint URL")
	}
	u.Path = path.Join(u.Path, signalPath)
	u.RawPath = ""
	return u.String(), nil
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
	if ConfigFromEnv(service).LogsEnabled {
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
