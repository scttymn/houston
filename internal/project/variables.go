package project

import (
	"sort"
	"strconv"
	"strings"
)

// A use is one ${VAR} or $VAR occurrence in the file.
type use struct {
	name       string
	path       string
	required   bool   // no default, or :? / ?
	hasDefault bool   // :- or -
	def        string // the default text, when hasDefault
}

// collectVariables finds every variable the file references outside
// x-houston, classifies each by its uses, and checks <SERVICE>_HOST uses.
// compose-go's ExtractVariables merges uses and can't tell ${V} from ${V:-},
// so this scans the same syntax itself.
func collectVariables(raw map[string]any, hosts map[string]string, ps *problems) []Variable {
	var uses []use
	for _, k := range sortedKeys(raw) {
		if k != "x-houston" {
			walk(raw[k], k, &uses)
		}
	}

	byName := map[string]*Variable{}
	for _, u := range uses {
		v, seen := byName[u.name]
		if !seen {
			v = &Variable{Name: u.name, Kind: Secret}
			if _, isHost := hosts[u.name]; isHost {
				v.Kind = ServiceHost
			}
			byName[u.name] = v
		}
		v.Required = v.Required || u.required
		v.BlankDefault = v.BlankDefault || (u.hasDefault && u.def == "")
		if service, isHost := hosts[u.name]; isHost && (!u.hasDefault || u.def != service) {
			ps.add(u.path, "write `${%s:-%s}` so plain `docker compose` finds the `%s` service; Houston sets %s on the server", u.name, service, service, u.name)
		}
	}

	var vars []Variable
	for _, v := range byName {
		v.BlankDefault = v.BlankDefault && !v.Required
		vars = append(vars, *v)
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
	return vars
}

func walk(v any, path string, uses *[]use) {
	switch v := v.(type) {
	case string:
		scan(v, path, uses)
	case map[string]any:
		for _, k := range sortedKeys(v) {
			walk(v[k], path+"."+k, uses)
		}
	case []any:
		for i, item := range v {
			walk(item, path+"["+itoa(i)+"]", uses)
		}
	}
}

// scan reads compose's interpolation syntax: $$ (escaped), $NAME, and
// ${NAME}, ${NAME:?msg}, ${NAME?msg}, ${NAME:-default}, ${NAME-default},
// ${NAME:+alt}, ${NAME+alt}, with variables nested inside defaults.
func scan(s, path string, uses *[]use) {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			continue
		}
		switch next := s[i+1]; {
		case next == '$':
			i++
		case next == '{':
			end := closingBrace(s, i+1)
			if end < 0 {
				return // unterminated; compose reports it when interpolating
			}
			scanBraced(s[i+2:end], path, uses)
			i = end
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			*uses = append(*uses, use{name: s[i+1 : j], path: path, required: true})
			i = j - 1
		}
	}
}

func scanBraced(inner, path string, uses *[]use) {
	n := 0
	for n < len(inner) && isNameChar(inner[n]) {
		n++
	}
	if n == 0 || !isNameStart(inner[0]) {
		return
	}
	u := use{name: inner[:n], path: path}
	op, tail := splitOperator(inner[n:])
	switch op {
	case "", ":?", "?":
		u.required = true
	case ":-", "-":
		u.hasDefault, u.def = true, tail
	case ":+", "+":
		// the alternative is used only when NAME is set: optional
	default:
		return
	}
	*uses = append(*uses, u)
	if !u.required {
		scan(tail, path, uses) // variables inside the default or alternative, as compose-go reads them
	}
}

// splitOperator splits ":-default" into ":-" and "default".
func splitOperator(rest string) (op, tail string) {
	for _, candidate := range []string{":?", ":-", ":+", "?", "-", "+"} {
		if strings.HasPrefix(rest, candidate) {
			return candidate, rest[len(candidate):]
		}
	}
	return rest, ""
}

func closingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

func itoa(i int) string { return strconv.Itoa(i) }
