package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	out := buf.String()
	if strings.Contains(out, "debug message") {
		t.Errorf("debug record must be filtered out at info level, got: %s", out)
	}
	if !strings.Contains(out, "info message") || !strings.Contains(out, "warn message") {
		t.Errorf("info and warn records must be present, got: %s", out)
	}
}

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)

	logger.Info("hello", "key", "value")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("output is not JSON: %v; got: %s", err, buf.String())
	}
	for _, field := range []string{"time", "level", "msg"} {
		if _, ok := record[field]; !ok {
			t.Errorf("record misses %q field: %v", field, record)
		}
	}
	if record["key"] != "value" {
		t.Errorf("attribute key = %v, want %q", record["key"], "value")
	}
}
