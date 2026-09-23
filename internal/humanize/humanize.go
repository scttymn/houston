// Package humanize writes sizes the way Mission Control's pages do.
package humanize

import (
	"fmt"
	"strconv"
)

// Bytes is n in 1024s with three significant digits, as Rails'
// number_to_human_size: 5 B, 391 MB, 1.5 GB.
func Bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, unit := range []string{"KB", "MB", "GB", "TB"} {
		v /= 1024
		if v < 1024 || unit == "TB" {
			return strconv.FormatFloat(v, 'g', 3, 64) + " " + unit
		}
	}
	return ""
}
