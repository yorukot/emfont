package logger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Level       string
	Environment string
	Service     string
	Version     string
	Development bool
	Encoding    string
}

type contextKey struct{}

var nop = zap.NewNop()

func New(cfg Config) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if cfg.Level != "" {
		if err := level.UnmarshalText([]byte(strings.ToLower(cfg.Level))); err != nil {
			return nil, fmt.Errorf("parse log level: %w", err)
		}
	}

	development := cfg.Development ||
		strings.EqualFold(cfg.Environment, "development") ||
		strings.EqualFold(cfg.Environment, "local") ||
		strings.EqualFold(cfg.Environment, "test")

	zapCfg := zap.NewProductionConfig()
	zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	if development {
		zapCfg = zap.NewDevelopmentConfig()
		zapCfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}

	if cfg.Encoding != "" {
		encoding := strings.ToLower(cfg.Encoding)
		if encoding == "text" {
			encoding = "console"
		}
		zapCfg.Encoding = encoding
	}
	zapCfg.Level = zap.NewAtomicLevelAt(level)

	fields := make([]zap.Field, 0, 3)
	if cfg.Service != "" {
		fields = append(fields, zap.String("service", cfg.Service))
	}
	if cfg.Version != "" {
		fields = append(fields, zap.String("version", cfg.Version))
	}
	if cfg.Environment != "" {
		fields = append(fields, zap.String("environment", cfg.Environment))
	}

	log, err := zapCfg.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
		zap.Fields(fields...),
	)
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}

	return log, nil
}

func Must(cfg Config) *zap.Logger {
	log, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return log
}

func Sync(log *zap.Logger) error {
	if log == nil {
		return nil
	}

	err := log.Sync()
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) {
		return nil
	}
	return err
}

func IntoContext(ctx context.Context, log *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = nop
	}
	return context.WithValue(ctx, contextKey{}, log)
}

func FromContext(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return nop
	}
	if log, ok := ctx.Value(contextKey{}).(*zap.Logger); ok && log != nil {
		return log
	}
	return nop
}

func With(ctx context.Context, fields ...zap.Field) context.Context {
	return IntoContext(ctx, FromContext(ctx).With(fields...))
}
