package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// evidence is what the test reports say about requirement IDs (Q-ROAD-4).
// A requirement is implemented when at least one passing test names it and no
// failing test names it.
type evidence struct {
	Passed map[string]bool
	Failed map[string]bool
	Tests  int // test results read, with or without an ID
}

func newEvidence() *evidence {
	return &evidence{Passed: map[string]bool{}, Failed: map[string]bool{}}
}

var requirementID = regexp.MustCompile(`R-[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]+`)

func idChar(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-'
}

// idsIn returns the requirement IDs a test name carries. An ID must stand
// alone: the characters around it may be anything but a letter, a digit or a
// hyphen, so `TestX/R-ARCH-002_forbidden_import` carries R-ARCH-002 while
// `R-ARCH-002-x` and `PR-ARCH-002` carry nothing.
func idsIn(name string) []string {
	var out []string
	for _, loc := range requirementID.FindAllStringIndex(name, -1) {
		if loc[0] > 0 && idChar(name[loc[0]-1]) {
			continue
		}
		if loc[1] < len(name) && idChar(name[loc[1]]) {
			continue
		}
		if id := name[loc[0]:loc[1]]; !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (e *evidence) record(name string, outcome string) {
	e.Tests++
	for _, id := range idsIn(name) {
		switch outcome {
		case "pass":
			e.Passed[id] = true
		case "fail":
			e.Failed[id] = true
		}
	}
}

// readEvidence reads one report. It detects the format from the first
// non-blank character: '<' is JUnit XML, anything else a `go test -json`
// stream. The name "-" reads standard input.
func (e *evidence) readFile(path string, stdin io.Reader) error {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // G304: the path is a report file named on the tool's command line
	}
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("%s: empty test report", path)
	}
	if trimmed[0] == '<' {
		err = e.readJUnit(data)
	} else {
		err = e.readGoJSON(data)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// readGoJSON reads a `go test -json` stream. Lines that are not JSON objects
// (build output mixed into the stream) are skipped; a stream without a single
// event is an error, so a wrong file cannot pass as "no evidence".
func (e *evidence) readGoJSON(data []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	events := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("not a go test -json stream: %w", err)
		}
		events++
		if ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass", "fail":
			e.record(ev.Test, ev.Action)
		case "skip":
			e.Tests++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if events == 0 {
		return errors.New("no go test -json events found")
	}
	return nil
}

type junitCase struct {
	Name      string   `xml:"name,attr"`
	Classname string   `xml:"classname,attr"`
	Failure   []string `xml:"failure"`
	Error     []string `xml:"error"`
	Skipped   []string `xml:"skipped"`
}

// readJUnit reads JUnit XML (<testsuites> or <testsuite> at the root). A
// testcase fails when it has a <failure> or <error> child, is skipped with a
// <skipped> child and passes otherwise. The class name and the name are both
// searched for IDs.
func (e *evidence) readJUnit(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	cases := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("not JUnit XML: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "testcase" {
			continue
		}
		var tc junitCase
		if err := dec.DecodeElement(&tc, &se); err != nil {
			return fmt.Errorf("not JUnit XML: %w", err)
		}
		cases++
		outcome := "pass"
		switch {
		case len(tc.Failure) > 0 || len(tc.Error) > 0:
			outcome = "fail"
		case len(tc.Skipped) > 0:
			outcome = "skip"
		}
		e.record(tc.Classname+" "+tc.Name, outcome)
	}
	if cases == 0 {
		return errors.New("no <testcase> elements found")
	}
	return nil
}

// implemented returns the requirement IDs with passing evidence and no
// failing test, restricted to the known requirements. IDs the reports name
// that no requirement has are returned as unknown (a typo in a test name
// would otherwise lose its evidence silently).
func (e *evidence) implemented(known map[string]bool) (ids []string, unknown []string) {
	seen := map[string]bool{}
	for id := range e.Passed {
		seen[id] = true
	}
	for id := range e.Failed {
		seen[id] = true
	}
	for id := range seen {
		if !known[id] {
			unknown = append(unknown, id)
			continue
		}
		if e.Passed[id] && !e.Failed[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(unknown)
	return ids, unknown
}

const implementedHeader = `# Requirements with passing tests that name them (Q-ROAD-4).
# Generated by internal/tools/coverage -tests; do not edit by hand.
# One requirement ID per line.
`

func renderImplemented(ids []string) string {
	var b strings.Builder
	b.WriteString(implementedHeader)
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return b.String()
}

// parseImplemented reads the committed list. Comment and blank lines are
// ignored; anything else must be a requirement ID.
func parseImplemented(text string) ([]string, error) {
	var ids []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !requirementID.MatchString(line) || requirementID.FindString(line) != line {
			return nil, fmt.Errorf("line %d: %q is not a requirement ID", i+1, line)
		}
		ids = append(ids, line)
	}
	sort.Strings(ids)
	return ids, nil
}
