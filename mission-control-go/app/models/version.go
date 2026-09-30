package models

import (
	"regexp"
	"strconv"
)

var release = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// NewerRelease is latest when it's a newer release than current (both
// vX.Y.Z, compared by number), else "": a server built from a checkout, or
// dev, is told of none.
func NewerRelease(current, latest string) string {
	c, l := release.FindStringSubmatch(current), release.FindStringSubmatch(latest)
	if c == nil || l == nil {
		return ""
	}
	for i := 1; i <= 3; i++ {
		cn, _ := strconv.Atoi(c[i])
		ln, _ := strconv.Atoi(l[i])
		if ln != cn {
			if ln > cn {
				return latest
			}
			return ""
		}
	}
	return ""
}
