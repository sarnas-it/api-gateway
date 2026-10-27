package logger

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"code.sarnas.ru/api-gateway/internal/config"
)

func testEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

func encodeEntry(t *testing.T, format string) string {
	t.Helper()
	encoder := newEncoder(format, testEncoderConfig())
	buf, err := encoder.EncodeEntry(
		zapcore.Entry{
			Level:   zapcore.InfoLevel,
			Time:    time.Unix(0, 0).UTC(),
			Message: "hello",
		},
		nil,
	)
	if err != nil {
		t.Fatalf("EncodeEntry(%q): %v", format, err)
	}
	return buf.String()
}

func TestNewEncoder_TextIsConsoleAlias(t *testing.T) {
	text := encodeEntry(t, "text")
	console := encodeEntry(t, "console")
	if text != console {
		t.Errorf("text and console must produce identical output:\n text=%q\n console=%q", text, console)
	}
	if !strings.Contains(text, "hello") || !strings.Contains(text, "INFO") {
		t.Errorf("unexpected console output: %q", text)
	}
}

func TestNewEncoder_FormatCaseInsensitive(t *testing.T) {
	if encodeEntry(t, "JSON") != encodeEntry(t, "json") {
		t.Error("JSON must encode identically to json")
	}
	if encodeEntry(t, "Console") != encodeEntry(t, "console") {
		t.Error("Console must encode identically to console")
	}
}

func TestNewEncoder_JSONFormat(t *testing.T) {
	jsonOut := encodeEntry(t, "json")
	if !strings.Contains(jsonOut, `"msg":"hello"`) {
		t.Errorf("json output missing msg field: %q", jsonOut)
	}
	if jsonOut == encodeEntry(t, "console") {
		t.Error("json output must differ from console")
	}
}

func TestNewZapLogger_AcceptsValidFormats(t *testing.T) {
	for _, format := range []string{"console", "text", "json"} {
		t.Run(format, func(t *testing.T) {
			l, err := NewZapLogger(&config.LoggingConfig{Level: "info", Format: format})
			if err != nil {
				t.Fatalf("NewZapLogger(%q) error: %v", format, err)
			}
			if l == nil {
				t.Fatalf("NewZapLogger(%q) returned nil logger", format)
			}
		})
	}
}
