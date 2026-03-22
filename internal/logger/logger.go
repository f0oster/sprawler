// Package logger provides structured logging with configurable levels.
package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// LogLevel represents logging verbosity levels.
type LogLevel int

// Log levels, ordered from least to most verbose.
const (
	LogLevelError LogLevel = iota // errors only
	LogLevelWarn                  // errors and warnings
	LogLevelInfo                  // default level
	LogLevelDebug                 // verbose diagnostics
	LogLevelTrace                 // maximum verbosity
)

// Logger provides structured logging with configurable levels.
type Logger struct {
	*log.Logger
	level LogLevel
	name  string
}

// NewLogger creates a logger for the named component.
func NewLogger(name string) *Logger {
	level := getLogLevelFromEnv()
	return &Logger{
		Logger: log.New(os.Stdout, fmt.Sprintf("[%-4s] ", name), log.Ltime),
		level:  level,
		name:   name,
	}
}

// getLogLevelFromEnv determines log level from LOG_LEVEL environment variable.
//
// Returns LogLevelInfo if LOG_LEVEL is unset or contains an invalid value.
func getLogLevelFromEnv() LogLevel {
	levelStr := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch levelStr {
	case "ERROR":
		return LogLevelError
	case "WARN", "WARNING":
		return LogLevelWarn
	case "INFO":
		return LogLevelInfo
	case "DEBUG":
		return LogLevelDebug
	case "TRACE":
		return LogLevelTrace
	default:
		return LogLevelInfo // Default level
	}
}

// Error logs error-level messages.
//
// Error messages are always displayed regardless of log level configuration.
func (l *Logger) Error(v ...interface{}) {
	if l.level >= LogLevelError {
		l.Logger.Printf("[ERROR] %s", fmt.Sprint(v...))
	}
}

// Errorf logs formatted error-level messages.
//
// Error messages are always displayed regardless of log level configuration.
func (l *Logger) Errorf(format string, v ...interface{}) {
	if l.level >= LogLevelError {
		l.Logger.Printf("[ERROR] "+format, v...)
	}
}

// Warn logs warning-level messages.
//
// Displayed when log level is WARN or higher.
func (l *Logger) Warn(v ...interface{}) {
	if l.level >= LogLevelWarn {
		l.Logger.Printf("[WARN] %s", fmt.Sprint(v...))
	}
}

// Warnf logs formatted warning-level messages.
//
// Displayed when log level is WARN or higher.
func (l *Logger) Warnf(format string, v ...interface{}) {
	if l.level >= LogLevelWarn {
		l.Logger.Printf("[WARN] "+format, v...)
	}
}

// Info logs informational messages.
//
// Displayed when log level is INFO or higher (default behavior).
func (l *Logger) Info(v ...interface{}) {
	if l.level >= LogLevelInfo {
		l.Logger.Printf("[INFO] %s", fmt.Sprint(v...))
	}
}

// Infof logs formatted informational messages.
//
// Displayed when log level is INFO or higher (default behavior).
func (l *Logger) Infof(format string, v ...interface{}) {
	if l.level >= LogLevelInfo {
		l.Logger.Printf("[INFO] "+format, v...)
	}
}

// Debug logs debug-level messages.
//
// Displayed only when log level is DEBUG or TRACE.
func (l *Logger) Debug(v ...interface{}) {
	if l.level >= LogLevelDebug {
		l.Logger.Printf("[DEBUG] %s", fmt.Sprint(v...))
	}
}

// Debugf logs formatted debug-level messages.
//
// Displayed only when log level is DEBUG or TRACE.
func (l *Logger) Debugf(format string, v ...interface{}) {
	if l.level >= LogLevelDebug {
		l.Logger.Printf("[DEBUG] "+format, v...)
	}
}

// Trace logs trace-level messages.
//
// Displayed only when log level is TRACE (highest verbosity).
func (l *Logger) Trace(v ...interface{}) {
	if l.level >= LogLevelTrace {
		l.Logger.Printf("[TRACE] %s", fmt.Sprint(v...))
	}
}

// Tracef logs formatted trace-level messages.
//
// Displayed only when log level is TRACE (highest verbosity).
func (l *Logger) Tracef(format string, v ...interface{}) {
	if l.level >= LogLevelTrace {
		l.Logger.Printf("[TRACE] "+format, v...)
	}
}

// Continuef prints a continuation line at INFO level, indented to align with
// the content of the previous log line (no prefix or timestamp).
func (l *Logger) Continuef(format string, v ...interface{}) {
	if l.level >= LogLevelInfo {
		// Prefix "[%-4s] " (8) + time "HH:MM:SS " (9) + "[INFO] " (7) = 24
		const pad = "                       "
		fmt.Printf(pad+format+"\n", v...)
	}
}
