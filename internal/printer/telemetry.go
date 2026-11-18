package printer

import (
	"encoding/json"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/event"
)

// sample is the part of one telemetry frame this package reads.
//
// A frame in a window is the CloudEvents envelope the host published, whole: its
// source is the origin of the device that reported and its data is the path and
// the value, {"path":"/job/progress","value":94}. The envelope carries the class,
// the sequence and the site beside them, none of which a reader of one printer
// needs.
type sample struct {
	Source string `json:"source"`
	Data   struct {
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"data"`
}

// Updates reads one window's telemetry and returns the patches that are about
// deviceID, in the order the window carried them.
//
// Frames about another device are dropped, as is a frame this build cannot read
// and one whose path is not an address in the tree. Both are silence rather than
// failure: a window is a fan-out of everything the subscriber may see, and one
// unreadable frame in it says nothing about the printer this package tracks.
//
// The window's telemetry lane is what to pass. It is the newest value of each path
// that moved during the window and never a whole tree, so the result is a patch
// list: apply it to what you hold, do not replace what you hold with it.
func Updates(frames []json.RawMessage, deviceID string) []Update {
	var updates []Update

	for _, frame := range frames {
		var decoded sample

		if err := json.Unmarshal(frame, &decoded); err != nil {
			continue
		}

		id, ok := deviceOf(decoded.Source)
		if !ok || id != deviceID {
			continue
		}

		if decoded.Data.Path == "" || decoded.Data.Path[0] != '/' || decoded.Data.Value == nil {
			continue
		}

		updates = append(updates, Update{Path: decoded.Data.Path, Value: decoded.Data.Value})
	}

	return updates
}

// deviceOf reads the device id out of a source URN, whose form is
// "urn:prusa:site:<site>/<kind>/<id>" (sdk/event.SourceURN). A source that is not
// a device origin, or has nothing after its last slash, is not one this package
// reads.
func deviceOf(source string) (string, bool) {
	slash := strings.LastIndexByte(source, '/')
	if slash < 0 || slash == len(source)-1 {
		return "", false
	}

	id := source[slash+1:]
	before := source[:slash]

	kindSlash := strings.LastIndexByte(before, '/')
	if kindSlash < 0 || before[kindSlash+1:] != event.KindDevice.String() {
		return "", false
	}

	return id, true
}
