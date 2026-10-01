// Package knownhosts reads the host keys Mission Control recorded for git
// hosts (its known_hosts file, written by ssh as it first reached each):
// runners trust only these, and never accept a new key themselves.
package knownhosts

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"regexp"
	"strings"
)

var (
	sshPort  = regexp.MustCompile(`^ssh://(?:[^@/]+@)?([^:/]+):(\d+)/`)
	sshURL   = regexp.MustCompile(`^ssh://(?:[^@/]+@)?([^:/]+)/`)
	scpStyle = regexp.MustCompile(`^[^@/]+@([^:/]+):`)
)

// For is file's lines for repoURL's SSH host, as they are in the file
// (ssh-keygen -F's): nil for HTTPS, an unseen host, or no file.
func For(file, repoURL string) []string {
	var host string
	if m := sshPort.FindStringSubmatch(repoURL); m != nil {
		host = "[" + m[1] + "]:" + m[2]
	} else if m := sshURL.FindStringSubmatch(repoURL); m != nil {
		host = m[1]
	} else if m := scpStyle.FindStringSubmatch(repoURL); m != nil {
		host = m[1]
	} else {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var found []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hosts, _, _ := strings.Cut(line, " ")
		if matches(hosts, host) {
			found = append(found, line)
		}
	}
	return found
}

// matches is whether a line's hosts field names host: a hashed name, or a
// comma-separated list of names and patterns (* and ?).
func matches(hosts, host string) bool {
	if rest, ok := strings.CutPrefix(hosts, "|1|"); ok {
		salt64, hash64, ok := strings.Cut(rest, "|")
		salt, err1 := base64.StdEncoding.DecodeString(salt64)
		hash, err2 := base64.StdEncoding.DecodeString(hash64)
		if !ok || err1 != nil || err2 != nil {
			return false
		}
		mac := hmac.New(sha1.New, salt)
		mac.Write([]byte(host))
		return hmac.Equal(mac.Sum(nil), hash)
	}
	for _, pattern := range strings.Split(hosts, ",") {
		if glob(strings.ToLower(pattern)).MatchString(strings.ToLower(host)) {
			return true
		}
	}
	return false
}

// glob is a host pattern as a regexp: * any run, ? one character, and
// everything else itself ([host]:port's brackets included).
func glob(pattern string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(quoted)
	return regexp.MustCompile("^" + quoted + "$")
}
