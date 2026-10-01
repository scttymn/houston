package models

import (
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata" // every zone, whatever the image has
)

var release = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// IsRelease is whether version is a release's tag, vX.Y.Z.
func IsRelease(version string) bool { return release.MatchString(version) }

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

// ValidTimeZone is whether zone is an IANA time zone's name (not "" or
// "Local", which Go's time package also takes).
func ValidTimeZone(zone string) bool {
	if zone == "" || zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}
