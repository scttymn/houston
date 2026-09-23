package cli

import (
	"errors"
	"regexp"
	"strings"
)

// defaultDockerfile is what `houston init` writes where there's no
// Dockerfile: the four stages Houston builds, serving the folder as a static
// site. The developer changes them for their project.
const defaultDockerfile = `# Houston builds the stage it needs: dev (houston dev), test (houston test),
# production (deploys). These defaults serve this folder as a static site;
# change them for your project.
FROM busybox:1.36 AS base
WORKDIR /app

FROM base AS dev
CMD ["httpd", "-f", "-v", "-p", "8080", "-h", "/app"]

FROM base AS test
COPY . /app

FROM base AS production
COPY . /app
CMD ["httpd", "-f", "-p", "8080", "-h", "/app"]
`

// FROM [--flag=…] image [AS name], on one logical instruction.
var fromRE = regexp.MustCompile(`(?i)^\s*FROM\s+(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?\s*$`)

var (
	directiveRE = regexp.MustCompile(`^#\s*([a-zA-Z]+)\s*=\s*(\S+)\s*$`)
	heredocRE   = regexp.MustCompile(`<<(-?)(["']?)([A-Za-z_][A-Za-z0-9_]*)(["']?)`)
)

// stage is a FROM instruction: its last physical line, and its name.
type stage struct {
	last int
	name string // as written; "" when unnamed
}

// stages reads the FROM instructions as Docker does: a line ending in the
// escape character (\, or what a leading `# escape=` directive sets) goes
// on to the next, comments and blank lines between instructions don't
// count, and heredoc bodies (RUN <<EOF … EOF) aren't instructions. lines
// have no \r; a UTF-8 BOM on the first line is ignored.
func stages(lines []string) []stage {
	escape := `\`
	i := 0
	for ; i < len(lines); i++ { // parser directives come first, before anything else
		m := directiveRE.FindStringSubmatch(strings.TrimPrefix(lines[i], "\ufeff"))
		if m == nil {
			break
		}
		if strings.EqualFold(m[1], "escape") && (m[2] == "`" || m[2] == `\`) {
			escape = m[2]
		}
	}
	var out []stage
	for ; i < len(lines); i++ {
		line := strings.TrimPrefix(lines[i], "\ufeff")
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// One instruction: a line ending in the escape character goes on to
		// the next one; comment and blank lines inside it don't count.
		text := line
		for continued := endsWith(text, escape); continued && i+1 < len(lines); {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), escape)
			i++
			next := strings.TrimSpace(lines[i])
			if next == "" || strings.HasPrefix(next, "#") {
				continue // still continued: the next line carries on
			}
			text += " " + lines[i]
			continued = endsWith(lines[i], escape)
		}
		if m := fromRE.FindStringSubmatch(text); m != nil {
			out = append(out, stage{last: i, name: m[2]})
		}
		for _, h := range heredocRE.FindAllStringSubmatch(text, -1) {
			for i+1 < len(lines) {
				i++
				body := lines[i]
				if h[1] == "-" {
					body = strings.TrimLeft(body, "\t")
				}
				if body == h[3] {
					break
				}
			}
		}
	}
	return out
}

func endsWith(line, escape string) bool {
	return strings.HasSuffix(strings.TrimRight(line, " \t"), escape)
}

// upsertDockerfile adds the stages Houston builds that src lacks: an unnamed
// final stage is named production; a missing production is made from the
// final stage; missing dev and test start as production. Every other line
// stays as it was, in the file's own line endings. summary is empty when
// nothing changed. The result is read again before it's returned.
func upsertDockerfile(src string) (content, summary string, err error) {
	// Lines keep their own endings: only what init writes uses the first
	// line's (CRLF or LF).
	raw := strings.Split(src, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	eol := "\n"
	if strings.HasSuffix(raw[0], "\r") {
		eol = "\r\n"
	}
	found := stages(lines)
	if len(found) == 0 {
		return "", "", errors.New("the Dockerfile has no FROM line; fix it, or move it aside and run houston init again for Houston's default")
	}
	names := map[string]bool{}
	for _, s := range found {
		names[strings.ToLower(s.name)] = true
	}
	final := found[len(found)-1]

	var did []string
	if final.name == "" && !names["production"] {
		raw[final.last] = strings.TrimRight(lines[final.last], " \t") + " AS production" + strings.TrimPrefix(raw[final.last], lines[final.last])
		final.name, names["production"] = "production", true
		did = append(did, "named the final stage production")
	}
	var added, more []string
	if !names["production"] {
		added, more = append(added, "production"), append(more, "FROM "+final.name+" AS production")
	}
	for _, s := range []string{"dev", "test"} {
		if !names[s] {
			added, more = append(added, s), append(more, "FROM production AS "+s)
		}
	}
	if len(did) == 0 && len(added) == 0 {
		return src, "", nil
	}

	content = strings.Join(raw, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += eol
	}
	if len(added) > 0 {
		note := "# Added by houston init: Houston deploys the production stage.\n"
		if len(added) > 1 || added[0] != "production" {
			note = "# Added by houston init: Houston builds dev (houston dev), test (houston test)\n" +
				"# and production (deploys). dev and test start as production: make them your\n" +
				"# project's own. Plain `docker build` now builds the last stage; pass\n" +
				"# --target production for the production image.\n"
		}
		content += strings.ReplaceAll("\n"+note+strings.Join(more, "\n\n")+"\n", "\n", eol)
		did = append(did, "added "+andList(added))
	}

	// Read what would be written: all three stages, or nothing is written.
	got := map[string]bool{}
	for _, s := range stages(strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")) {
		got[strings.ToLower(s.name)] = true
	}
	if !got["dev"] || !got["test"] || !got["production"] {
		return "", "", errors.New("houston init couldn't complete this Dockerfile's stages; add dev, test and production stages by hand")
	}
	return content, strings.Join(did, "; "), nil
}

// andList is "a", "a and b", or "a, b and c".
func andList(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
