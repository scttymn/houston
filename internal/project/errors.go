package project

import (
	"sort"
	"strings"
)

// Problem is one thing wrong with the file. Path is the key path
// ("services.app.network_mode"), or empty for the file as a whole.
type Problem struct {
	Path string
	Msg  string
}

// Errors is every problem Load found, sorted by path.
type Errors struct {
	File     string
	Problems []Problem
}

func (e *Errors) Error() string {
	var b strings.Builder
	for _, p := range e.Problems {
		b.WriteString(e.File)
		b.WriteString(": ")
		if p.Path != "" {
			b.WriteString(p.Path)
			b.WriteString(": ")
		}
		b.WriteString(p.Msg)
		b.WriteString("\n")
	}
	return b.String()
}

type problems []Problem

func (ps *problems) add(path, format string, args ...any) {
	*ps = append(*ps, Problem{Path: path, Msg: sprintf(format, args...)})
}

func (ps problems) errors(file string) *Errors {
	sorted := append([]Problem(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	return &Errors{File: file, Problems: sorted}
}
