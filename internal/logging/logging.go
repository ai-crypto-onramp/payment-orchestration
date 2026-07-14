package logging

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a log entry.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// levelOrder maps levels to numeric severity for comparison.
var levelOrder = map[Level]int{
	LevelDebug: 0,
	LevelInfo:  1,
	LevelWarn:  2,
	LevelError: 3,
}

// ParseLevel parses a level string, defaulting to LevelInfo.
func ParseLevel(s string) Level {
	l := Level(strings.ToLower(s))
	switch l {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		return l
	}
	return LevelInfo
}

// Logger is a structured JSON logger that writes one entry per line. It is
// safe for concurrent use.
type Logger struct {
	mu     sync.Mutex
	w      io.Writer
	level  Level
	fields map[string]interface{}
}

// New returns a Logger that writes to w at the given level. A nil writer
// disables output (useful for tests).
func New(w io.Writer, level Level) *Logger {
	if w == nil {
		w = io.Discard
	}
	return &Logger{w: w, level: level, fields: make(map[string]interface{})}
}

// NewDefault returns a Logger writing to stdout at the level parsed from the
// LOG_LEVEL env var.
func NewDefault() *Logger {
	return New(os.Stdout, ParseLevel(os.Getenv("LOG_LEVEL")))
}

// With returns a child Logger with the given fields merged in. The parent
// logger is unchanged.
func (l *Logger) With(fields map[string]interface{}) *Logger {
	out := &Logger{w: l.w, level: l.level, fields: make(map[string]interface{}, len(l.fields)+len(fields))}
	for k, v := range l.fields {
		out.fields[k] = v
	}
	for k, v := range fields {
		out.fields[k] = v
	}
	return out
}

// Log writes an entry at the given level if it meets the configured threshold.
func (l *Logger) Log(level Level, msg string, fields map[string]interface{}) {
	if levelOrder[level] < levelOrder[l.level] {
		return
	}
	entry := make(map[string]interface{}, len(l.fields)+len(fields)+3)
	for k, v := range l.fields {
		entry[k] = v
	}
	for k, v := range fields {
		entry[k] = v
	}
	entry["level"] = string(level)
	entry["msg"] = msg
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(entry)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string, fields map[string]interface{}) { l.Log(LevelDebug, msg, fields) }

// Info logs at info level.
func (l *Logger) Info(msg string, fields map[string]interface{}) { l.Log(LevelInfo, msg, fields) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, fields map[string]interface{}) { l.Log(LevelWarn, msg, fields) }

// Error logs at error level.
func (l *Logger) Error(msg string, fields map[string]interface{}) { l.Log(LevelError, msg, fields) }

// redactedKeys is the set of keys whose values must never be logged.
var redactedKeys = map[string]bool{
	"pan":            true,
	"card_number":    true,
	"card_token":     true,
	"track":          true,
	"cvv":            true,
	"cvc":            true,
	"webhook_secret": true,
	"authorization":  true,
}

// Redact returns "[redacted]" for any field whose key matches a known
// sensitive name (case-insensitive). Use it when constructing log fields
// from request data.
func Redact(key string) string {
	if redactedKeys[strings.ToLower(key)] {
		return "[redacted]"
	}
	return key
}