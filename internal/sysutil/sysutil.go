package sysutil

import (
	"strings"

	"github.com/rs/zerolog"
)

// setLogLevel configures the global zerolog level based on a string value.
// Supported values (case-insensitive): debug, info, warn, error, fatal, panic.
func SetLogLevel(lvl string) {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info", "":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn", "warning":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	case "fatal":
		zerolog.SetGlobalLevel(zerolog.FatalLevel)
	case "panic":
		zerolog.SetGlobalLevel(zerolog.PanicLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

// isTruthy reports whether an environment variable string should be considered true.
// Accepted values (case-insensitive): "1", "true", "yes", "y", "on".
func IsTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// firstNonEmpty returns the first non-empty string from a variadic list.
// If all values are empty, it returns "".
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
