package utils

import "testing"

func TestAtoiDefault(t *testing.T) {
	cases := []struct {
		s    string
		def  int
		want int
	}{
		// empty -> default
		{"", 10, 10},
		// valid ints
		{"42", 0, 42},
		{"-13", 1, -13},
		{"0012", 99, 12},
		// invalid -> default (no trim)
		{"x", 5, 5},
		{" 42", 7, 7},
		// overflow -> default
		{"999999999999999999999999", -1, -1},
	}

	for _, tc := range cases {
		if got := AtoiDefault(tc.s, tc.def); got != tc.want {
			t.Fatalf("AtoiDefault(%q, %d) = %d; want %d", tc.s, tc.def, got, tc.want)
		}
	}
}
