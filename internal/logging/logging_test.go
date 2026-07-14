package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := map[string]Level{
		"debug": LevelDebug,
		"INFO":  LevelInfo,
		"warn":   LevelWarn,
		"error":  LevelError,
		"":       LevelInfo,
		"bogus":  LevelInfo,
	}
	for in, want := range tests {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelWarn)
	l.Debug("ignored", nil)
	l.Info("ignored-too", nil)
	l.Warn("kept", nil)
	l.Error("kept-too", nil)
	out := buf.String()
	if strings.Contains(out, "ignored") {
		t.Fatalf("debug/info should not be written: %s", out)
	}
	if !strings.Contains(out, "kept") || !strings.Contains(out, "kept-too") {
		t.Fatalf("warn/error should be written: %s", out)
	}
}

func TestLoggerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelInfo)
	l.Info("hello", map[string]interface{}{"k": "v"})
	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if entry["msg"] != "hello" {
		t.Fatalf("msg = %v", entry["msg"])
	}
	if entry["level"] != "info" {
		t.Fatalf("level = %v", entry["level"])
	}
	if entry["k"] != "v" {
		t.Fatalf("k = %v", entry["k"])
	}
	if _, ok := entry["ts"]; !ok {
		t.Fatal("missing ts")
	}
}

func TestLoggerWith(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelInfo).With(map[string]interface{}{"svc": "po"})
	l.Info("hi", nil)
	var entry map[string]interface{}
	_ = json.Unmarshal(buf.Bytes(), &entry)
	if entry["svc"] != "po" {
		t.Fatalf("svc = %v", entry["svc"])
	}
}

func TestRedact(t *testing.T) {
	if Redact("pan") != "[redacted]" {
		t.Fatal("pan should be redacted")
	}
	if Redact("PAN") != "[redacted]" {
		t.Fatal("PAN should be redacted (case-insensitive)")
	}
	if Redact("amount") != "amount" {
		t.Fatal("amount should not be redacted")
	}
}