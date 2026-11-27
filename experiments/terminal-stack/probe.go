package stack

import (
	"github.com/charmbracelet/x/vt"
)

type CandidateProbe struct {
	Name             string
	Passed           bool
	Expected, Actual string
}

// ProbeCandidate reports feasibility gaps independently of the driver's tests.
// A green driver suite does not turn a failed dependency probe into support.
func ProbeCandidate() []CandidateProbe {
	var results []CandidateProbe
	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"whole combining grapheme", []string{"e\u0301"}, "e\u0301"},
		{"combining grapheme across PTY reads", []string{"e", "\u0301"}, "e\u0301"},
		{"wide UTF-8 across PTY reads", []string{"\xe7", "\x95", "\x8c"}, "界"},
		{"ZWJ emoji across PTY reads", []string{"👩", "\u200d", "💻"}, "👩\u200d💻"},
	} {
		e := vt.NewEmulator(20, 4)
		for _, chunk := range tc.chunks {
			_, _ = e.WriteString(chunk)
		}
		actual := e.CellAt(0, 0).Content
		results = append(results, CandidateProbe{Name: tc.name, Passed: actual == tc.want, Expected: tc.want, Actual: actual})
		_ = e.Close()
	}
	return results
}
