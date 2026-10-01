package app

import "regexp"

var (
	versionTag = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)
	versionSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// HoustonVersion is which Houston this is: a release's tag, baked into the
// published image (HOUSTON_VERSION); a checkout's commit, when the installer
// built the image from source (HOUSTON_SOURCE_SHA); or dev.
func HoustonVersion(tag, sha string) string {
	switch {
	case versionTag.MatchString(tag):
		return tag
	case versionSHA.MatchString(sha):
		return "source " + sha[:7]
	}
	return "dev"
}
