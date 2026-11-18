package printer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// checkValue reads one value the way a snapshot will read it, so a path this
// package can present is a path it accepts. A path it has no reading of is
// accepted as it stands: the state tree is open, and a node a newer driver
// invented is still a fact about the printer.
func checkValue(path string, value json.RawMessage) error {
	switch path {
	case PathState, PathJobState, PathJobDisplay, PathJobPath, PathMaterial:
		_, err := textOf(value)

		return err
	case PathJobProgress, PathNozzleTemp, PathNozzleTarget, PathBedTemp, PathBedTarget:
		_, err := numberOf(value)

		return err
	case PathJobRemaining, PathJobPrinting:
		_, err := secondsOf(value)

		return err
	}

	return nil
}

// textReading reads one text path, and leaves it unknown when it is absent or
// this build cannot read it. The second case cannot arise for a path checkValue
// passed, and it is here so a snapshot never fails on a tree it did not build.
func textReading(tree map[string]json.RawMessage, path string) Reading[string] {
	value, ok := tree[path]
	if !ok {
		return Reading[string]{}
	}

	text, err := textOf(value)
	if err != nil {
		return Reading[string]{}
	}

	return Reading[string]{Value: text, Known: true}
}

// numberReading reads one number path on the same terms.
func numberReading(tree map[string]json.RawMessage, path string) Reading[float64] {
	value, ok := tree[path]
	if !ok {
		return Reading[float64]{}
	}

	number, err := numberOf(value)
	if err != nil {
		return Reading[float64]{}
	}

	return Reading[float64]{Value: number, Known: true}
}

// secondsReading reads one path the machine reports as whole seconds on the same
// terms, as a duration.
func secondsReading(tree map[string]json.RawMessage, path string) Reading[time.Duration] {
	value, ok := tree[path]
	if !ok {
		return Reading[time.Duration]{}
	}

	seconds, err := secondsOf(value)
	if err != nil {
		return Reading[time.Duration]{}
	}

	return Reading[time.Duration]{Value: seconds, Known: true}
}

// textOf reads a JSON string. null is the printer taking the value away and is
// reported as errNoValue, which is not a failure.
func textOf(value json.RawMessage) (string, error) {
	if isNull(value) {
		return "", errNoValue
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", fmt.Errorf("this build reads text here and the value is not text: %w", err)
	}

	return text, nil
}

// numberOf reads a JSON number. A machine that reports a whole percent as an
// integer, such as 94, and one that reports 94.5 both arrive here as a float.
func numberOf(value json.RawMessage) (float64, error) {
	if isNull(value) {
		return 0, errNoValue
	}

	var number float64
	if err := json.Unmarshal(value, &number); err != nil {
		return 0, fmt.Errorf("this build reads a number here and the value is not one: %w", err)
	}

	return number, nil
}

// secondsOf reads a JSON number of seconds as a duration.
//
// A negative count is refused rather than presented. Several firmwares answer
// -1 for "I do not know how long is left", and counting that down as a figure a
// second before now would be this package inventing a value the printer did not
// report. A count too large for a duration is refused for the same reason: it
// would wrap into a number nothing measured.
func secondsOf(value json.RawMessage) (time.Duration, error) {
	seconds, err := numberOf(value)
	if err != nil {
		return 0, err
	}

	switch {
	case seconds < 0:
		return 0, fmt.Errorf("the printer reported %v seconds, and a time before now is not a reading", seconds)
	case seconds > float64(math.MaxInt64)/float64(time.Second):
		return 0, fmt.Errorf("the printer reported %v seconds, which is longer than a duration holds", seconds)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

// isNull reports whether a value is JSON null, which is the printer saying it has
// no value there rather than a value of zero.
func isNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

// clip cuts a value to something an error line can carry, on a rune boundary. The
// whole of it stays in the tree.
func clip(value json.RawMessage) json.RawMessage {
	const limit = 32

	if len(value) <= limit {
		return append(json.RawMessage(nil), value...)
	}

	cut := value[:limit]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}

	return append(append(json.RawMessage(nil), cut...), "..."...)
}
