package sysutil

import (
	"testing"

	"github.com/rs/zerolog"
)

func TestSetLogLevel_AllVariants(t *testing.T) {
	orig := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(orig) })

	cases := []struct {
		in   string
		want zerolog.Level
	}{
		{"debug", zerolog.DebugLevel},
		{"  DeBuG  ", zerolog.DebugLevel}, // case + trim
		{"info", zerolog.InfoLevel},
		{"", zerolog.InfoLevel}, // empty -> info
		{"warn", zerolog.WarnLevel},
		{"warning", zerolog.WarnLevel}, // alias
		{"error", zerolog.ErrorLevel},
		{"fatal", zerolog.FatalLevel},
		{"panic", zerolog.PanicLevel},
		{"unknown", zerolog.InfoLevel}, // default
	}

	for _, tc := range cases {
		SetLogLevel(tc.in)
		if got := zerolog.GlobalLevel(); got != tc.want {
			t.Fatalf("SetLogLevel(%q) -> %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	trues := []string{"1", "true", "TRUE", " yes ", "Y", "on", "On"}
	falses := []string{"", "0", "false", "no", "off", "n", "  ", "random"}

	for _, v := range trues {
		if !IsTruthy(v) {
			t.Fatalf("IsTruthy(%q) = false; want true", v)
		}
	}
	for _, v := range falses {
		if IsTruthy(v) {
			t.Fatalf("IsTruthy(%q) = true; want false", v)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	// no args -> ""
	if got := FirstNonEmpty(); got != "" {
		t.Fatalf("FirstNonEmpty() = %q; want \"\"", got)
	}
	// only empties -> ""
	if got := FirstNonEmpty(" ", "\t", "\n"); got != "" {
		t.Fatalf("FirstNonEmpty(empties) = %q; want \"\"", got)
	}
	// picks first non-empty (preserves original spacing)
	if got := FirstNonEmpty("   ", "  hello  ", "world"); got != "  hello  " {
		t.Fatalf("FirstNonEmpty(...) = %q; want %q", got, "  hello  ")
	}
	// first already non-empty
	if got := FirstNonEmpty("alpha", "beta"); got != "alpha" {
		t.Fatalf("FirstNonEmpty(...) = %q; want %q", got, "alpha")
	}
}
