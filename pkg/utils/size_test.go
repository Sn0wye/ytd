package utils

import "testing"

func TestFormatSize(t *testing.T) {
	for b, want := range map[int64]string{0: "0 B", 1 << 20: "1.0 MB", 351_000_000: "334.7 MB", 3 << 30: "3.00 GB"} {
		if got := FormatSize(b); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", b, got, want)
		}
	}
}
