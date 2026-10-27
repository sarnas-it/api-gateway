package logger

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"code.sarnas.ru/api-gateway/internal/config"
)

// NewZapLogger создает новый zap логгер на основе конфигурации
func NewZapLogger(cfg *config.LoggingConfig) (*zap.Logger, error) {
	// Настройка уровня логирования
	var level zapcore.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	case "panic":
		level = zapcore.PanicLevel
	case "fatal":
		level = zapcore.FatalLevel
	default:
		level = zapcore.InfoLevel
	}

	// Настройка энкодера
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder, // INFO, ERROR и т.д.
		EncodeTime:     zapcore.ISO8601TimeEncoder,  // 2024-01-02T15:04:05Z
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder, // путь/файл:строка
	}

	// Выбор формата: json, console, text (алиас console).
	encoder := newEncoder(cfg.Format, encoderConfig)

	// Создаем ядро
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(os.Stdout),
		level,
	)

	// Создаем логгер с добавлением caller
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return logger, nil
}

// newEncoder выбирает энкодер по формату логирования. Валидные значения:
// json, console и text (алиас console). Любое другое значение трактуется как
// консольный вывод — конфиг валидируется отдельно (config.validate).
func newEncoder(format string, encoderConfig zapcore.EncoderConfig) zapcore.Encoder {
	if strings.ToLower(format) == "json" {
		return zapcore.NewJSONEncoder(encoderConfig)
	}
	// console и text выводят одинаковый человекочитаемый формат с цветами.
	encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	return zapcore.NewConsoleEncoder(encoderConfig)
}

// MustZapLogger создает логгер или паникует при ошибке
func MustZapLogger(cfg *config.LoggingConfig) *zap.Logger {
	logger, err := NewZapLogger(cfg)
	if err != nil {
		panic("failed to create logger: " + err.Error())
	}
	return logger
}
