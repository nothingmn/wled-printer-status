// Package printer tracks one printer through Prusa Connect and says what is
// known about it together with how old that knowledge is.
//
// It holds no presentation and no policy. It does not decide what colour a
// paused print is, how often a display refreshes, or whether anything should
// reach a light. It answers three questions: what the printer last reported,
// when that last changed, and whether the answer still describes the printer.
//
// # The two views a host offers, and why neither expires
//
// A module reads a printer two ways. deviceapi.Client.Get answers the record the
// host holds — a typed Device carrying Connected, Issues, Dialog, and the
// addressable state tree under State map[string]json.RawMessage, keyed by path.
// busapi.Subscribe answers a stream of windows, each carrying the newest value
// of every path that moved since the window before (Window.Telemetry) and
// nothing at all for a path that did not move.
//
// So the stream is a patch stream and the record is a snapshot, and the host
// expires neither:
//
//   - Device.State is documented as "the addressable state tree the device last
//     reported" (sdk/deviceapi/wire.go). A printer that went away keeps the tree
//     it had, and nothing in the host dates it.
//   - /state carries ten values — IDLE, BUSY, READY, PRINTING, PAUSED, FINISHED,
//     STOPPED, ATTENTION, ERROR, UNKNOWN — and OFFLINE is not among them.
//     Nothing in the tree means "gone".
//   - Device.Connected is the only field that says gone, and it is the host's
//     answer about a session rather than the printer's word.
//
// Put together, a caller that reads the tree and nothing else will say
// "PRINTING, 94%, six minutes left" about a printer that was switched off at
// 14:02 and has not been heard from since. That sentence is why this package
// exists.
//
// # The offline rule
//
// When Connected is false the snapshot reports not-connected, and no reading in
// it is presented as describing the printer now:
//
//   - Snapshot.Connected is false, from the host's own record.
//   - Job.Live is false, so every number in Job is a memory.
//   - LiveProgress and LiveRemaining answer ok=false, which is the pair a caller
//     should render from: there is no way to reach a progress figure through them
//     that is not a present-tense reading.
//   - The last readings are still there, because they are facts. "Last reported
//     94% at 14:02" is what Snapshot.Job.Progress.Value and Snapshot.LastChange
//     say between them, and Snapshot.Age says how long ago that was.
//
// The same bar applies to a printer that is connected but has gone quiet: a
// reading older than StaleAfter sets Stale and clears Job.Live, so a tree the
// module has not heard a change in for half a minute is not shown as a live job.
//
// # What the reducer refuses to do
//
//   - It never invents a value. A path the printer has not written, and a path it
//     wrote null to, are unknown — not zero, not empty text, not IDLE.
//   - It accepts a path this build has never seen and keeps it as reported, so a
//     newer firmware's node reaches a caller instead of failing the batch.
//   - A value this build cannot read is reported as a *ValueError and the last
//     value it could read for that path is kept. One odd number never costs the
//     paths behind it.
//
// # What is not here
//
// No WLED client, no colour, no layout, no polling policy beyond the read
// interval a caller sets. It is not wired into main.go, and it has never run
// against a live host: no bus exists until a module is installed and serving, so
// the subscription and the read are exercised against fakes only.
package printer
