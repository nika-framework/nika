package nikatest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// goldenDir is where snapshots live, relative to the test's package directory.
// The `testdata` name is special to the go tool: it is excluded from builds and
// from package matching, so snapshots never end up compiled or published.
const goldenDir = "testdata/golden"

// updateGoldenEnv, when set to a truthy value, rewrites snapshots instead of
// comparing against them.
//
// An environment variable rather than a flag: registering a flag from a library
// package would collide with the test binary's own flags and change the meaning
// of `go test ./...` for every package that imports this one.
const updateGoldenEnv = "NIKA_UPDATE_GOLDEN"

// ExpectGolden compares the response body against a stored snapshot, which is
// the practical way to test rendered content — an HTML page, a CSV export, a
// generated report — where writing the expectation inline would be unreadable.
//
// Run the suite with NIKA_UPDATE_GOLDEN=1 to record or refresh a snapshot, then
// review the diff before committing it. A snapshot nobody reads is not a test.
func (r *Response) ExpectGolden(name string) *Response {
	r.t.Helper()
	compareGolden(r.t, name, r.raw)
	return r
}

// ExpectGoldenJSON compares the body against a snapshot after normalising it as
// indented JSON, so a change in key order or whitespace does not fail the test.
func (r *Response) ExpectGoldenJSON(name string) *Response {
	r.t.Helper()

	normalized, err := indentJSON(r.raw)
	if err != nil {
		r.t.Fatalf("%s: body is not valid JSON: %v\n  body: %s", r.describe(), err, r.bodyForError())
		return r
	}
	compareGolden(r.t, name, normalized)
	return r
}

// Scrubber replaces volatile substrings before a snapshot comparison.
type Scrubber struct {
	pattern     *regexp.Regexp
	replacement string
}

// Scrub builds a Scrubber from a regular expression.
func Scrub(pattern, replacement string) Scrubber {
	return Scrubber{pattern: regexp.MustCompile(pattern), replacement: replacement}
}

// Common scrubbers for the values that change on every run and would otherwise
// make every snapshot test fail the second time it runs.
var (
	ScrubUUID = Scrub(
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
		"<uuid>")
	ScrubObjectID = Scrub(`\b[0-9a-f]{24}\b`, "<objectid>")
	ScrubRFC3339  = Scrub(
		`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`,
		"<timestamp>")
	ScrubJWT = Scrub(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`, "<jwt>")
)

// ExpectGoldenScrubbed compares against a snapshot after applying the scrubbers,
// so generated ids and timestamps do not make the test non-deterministic.
//
//	res.ExpectGoldenScrubbed("user_created",
//	    nikatest.ScrubObjectID, nikatest.ScrubRFC3339)
func (r *Response) ExpectGoldenScrubbed(name string, scrubbers ...Scrubber) *Response {
	r.t.Helper()

	body := r.raw
	for _, scrubber := range scrubbers {
		body = scrubber.pattern.ReplaceAll(body, []byte(scrubber.replacement))
	}
	compareGolden(r.t, name, body)
	return r
}

// compareGolden reads or writes the snapshot and reports a diff.
func compareGolden(t TB, name string, actual []byte) {
	t.Helper()

	path := goldenPath(name)

	if updatingGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("nikatest: cannot create %s: %v", filepath.Dir(path), err)
			return
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatalf("nikatest: cannot write snapshot %s: %v", path, err)
			return
		}
		t.Logf("nikatest: snapshot %s updated (%d bytes) — review the diff before committing", path, len(actual))
		return
	}

	expected, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("nikatest: snapshot %s does not exist.\n  Run the test with %s=1 to create it:\n    %s=1 go test ./...",
				path, updateGoldenEnv, updateGoldenEnv)
			return
		}
		t.Fatalf("nikatest: cannot read snapshot %s: %v", path, err)
		return
	}

	if bytes.Equal(expected, actual) {
		return
	}

	t.Errorf("nikatest: response does not match snapshot %s\n%s\n  Re-record with: %s=1 go test -run %s ./...",
		path, lineDiff(string(expected), string(actual)), updateGoldenEnv, t.Name())
}

// goldenPath maps a snapshot name to a file, refusing anything that could escape
// the snapshot directory. A test name reaches this function, and a name like
// "../../.github/workflows/ci" would otherwise let a snapshot update overwrite a
// file outside testdata.
func goldenPath(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)

	if safe == "" {
		safe = "snapshot"
	}
	return filepath.Join(goldenDir, safe+".golden")
}

func updatingGolden() bool {
	switch strings.ToLower(os.Getenv(updateGoldenEnv)) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
}

func indentJSON(raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	// MarshalIndent sorts object keys, which is what makes the snapshot stable
	// across Go map iteration order.
	return json.MarshalIndent(value, "", "  ")
}

// lineDiff renders a compact line-by-line difference. It is not a full diff
// algorithm — for a snapshot mismatch, pointing at the first differing lines and
// showing both sides is what actually helps.
func lineDiff(expected, actual string) string {
	expectedLines := strings.Split(expected, "\n")
	actualLines := strings.Split(actual, "\n")

	var b strings.Builder
	limit := max(len(expectedLines), len(actualLines))
	const contextLines = 40
	shown := 0

	for i := 0; i < limit && shown < contextLines; i++ {
		expectedLine := lineAt(expectedLines, i)
		actualLine := lineAt(actualLines, i)
		if expectedLine == actualLine {
			continue
		}
		b.WriteString("  line ")
		b.WriteString(itoa(i + 1))
		b.WriteString(":\n    - want: ")
		b.WriteString(truncate(expectedLine))
		b.WriteString("\n    + got:  ")
		b.WriteString(truncate(actualLine))
		b.WriteString("\n")
		shown++
	}

	if shown == 0 {
		// Same lines but unequal bytes: a trailing-newline or line-ending
		// difference, which is invisible in a line diff.
		b.WriteString("  (only line endings or trailing whitespace differ)\n")
	}
	return b.String()
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(missing)"
}

func truncate(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
