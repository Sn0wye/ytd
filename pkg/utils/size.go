package utils

import "fmt"

// FormatSize formats a byte count as MB, or GB from 1 GB up.
func FormatSize(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	mb := float64(b) / (1 << 20)
	if mb >= 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.1f MB", mb)
}
