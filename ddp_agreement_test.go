package main

// The two backends, proved to draw the same picture.
//
// A frame can reach this operator's panel in two ways from here on: as a JSON body that
// names two rectangles and lets the controller work out the pixels, which is what the
// module sends today, and as a buffer of pixels over DDP, which is the transport a drawn
// layout needs. Both are meant to show the same thing, and nothing on either wire says
// whether they do — so this test watches both and compares them.
//
// It is deliberately end to end on the JSON side. The frame goes through render.Decide,
// then through the writer's own paint, then through a real wled.Client into an httptest
// server, and the picture is read back out of the bytes the controller would have
// received. On the other side it goes through matrix.Raster, which is the function DDP
// sends from. The two buffers are then compared pixel by pixel.
//
// What makes that worth having: the fill boundary is stated twice — fillColumns in this
// package and matrix.FillColumns — because the painter predates the pixel path and cannot
// call a package it does not import. This test is what keeps the two statements equal. When
// the composer lands and the painter calls matrix, it will guard one statement instead of
// two, and it should stay either way.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/matrix"
	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// percentages are the fractions the two backends are compared at: both edges, where there
// is no bar at all, and three points between them where a bar has a boundary a person can
// judge.
var percentages = []float64{0, 10, 62, 88, 100}

// printingAt is a printer printing at progress percent, connected and fresh, at the moment
// given. It is the frame's own input, so that the frame compared below is one render.Decide
// produced rather than one a test assembled to suit itself.
func printingAt(progress float64, at time.Time) printer.Snapshot {
	return printer.Snapshot{
		ID:        "01J8DEVICE",
		Name:      "Morpheus",
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: progress, Known: true},
		},
		LastChange: at,
		ObservedAt: at,
	}
}

// jsonBody is the part of POST /json/state this test reads back: the segments, as the
// controller reads them. Every field is a pointer for the same reason the client's own body
// uses one — "not named in this write" is not zero — and a segment that is off names neither
// bounds nor colour.
type jsonBody struct {
	Segments []struct {
		ID     int     `json:"id"`
		Start  *int    `json:"start"`
		Stop   *int    `json:"stop"`
		StartY *int    `json:"startY"`
		StopY  *int    `json:"stopY"`
		On     *bool   `json:"on"`
		Colour [][]int `json:"col"`
	} `json:"seg"`
}

// jsonPicture is the pixels one JSON body describes, as the controller would draw them:
// each segment in the order the body lists it, over the columns and rows it names. A
// segment that is off draws nothing, which is how the body clears a remainder rather than
// colouring one.
//
// A segment with no bounds at all keeps whatever rectangle the controller already holds for
// it, and the only such segment here is the switched-off remainder, so it is skipped rather
// than guessed at.
func jsonPicture(t *testing.T, body jsonBody, geometry panel) []wled.RGB {
	t.Helper()

	picture := make([]wled.RGB, geometry.width*geometry.height)

	for _, segment := range body.Segments {
		if segment.On == nil || !*segment.On {
			continue
		}

		if segment.Start == nil || segment.Stop == nil || segment.StartY == nil || segment.StopY == nil {
			t.Fatalf("segment %d is drawn and names no rectangle: %+v", segment.ID, segment)
		}

		if len(segment.Colour) == 0 || len(segment.Colour[0]) < 3 {
			t.Fatalf("segment %d is drawn and names no colour: %+v", segment.ID, segment)
		}

		colour := wled.RGB{
			R: uint8(segment.Colour[0][0]),
			G: uint8(segment.Colour[0][1]),
			B: uint8(segment.Colour[0][2]),
		}

		if *segment.Start < 0 || *segment.Stop > geometry.width ||
			*segment.StartY < 0 || *segment.StopY > geometry.height {
			t.Fatalf("segment %d covers columns %d..%d over rows %d..%d, which is not on a %dx%d panel",
				segment.ID, *segment.Start, *segment.Stop, *segment.StartY, *segment.StopY,
				geometry.width, geometry.height)
		}

		for row := *segment.StartY; row < *segment.StopY; row++ {
			for column := *segment.Start; column < *segment.Stop; column++ {
				picture[row*geometry.width+column] = colour
			}
		}
	}

	return picture
}

// bodyFor is the JSON body the module sends for one frame, captured from the wire: the
// writer paints through a real client, the client POSTs to a server in this process, and
// the request body is kept and decoded.
func bodyFor(t *testing.T, frame render.Frame, geometry panel) jsonBody {
	t.Helper()

	var bodies [][]byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}

		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		// WLED accepts a write and answers with the state it applied; an empty document is
		// enough for a client that only checks the status.
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	client, err := wled.New(server.URL, wled.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("the client for the test server was refused: %v", err)
	}

	pen := newWriter(client, 128, quiet)
	pen.setPanel(wled.Info{MatrixWidth: geometry.width, MatrixHeight: geometry.height})

	if err := pen.paint(context.Background(), frame); err != nil {
		t.Fatalf("the JSON path refused the frame: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(bodies) != 1 {
		t.Fatalf("the JSON path made %d requests, want one body carrying both segments", len(bodies))
	}

	var body jsonBody
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatalf("the body the module sent does not decode: %v\n%s", err, bodies[0])
	}

	return body
}

// TestTheJSONBodyAndTheDDPPixelsAreTheSamePicture is the claim the second backend rests on:
// for one frame, the pixels the JSON body describes and the pixels the DDP path would send
// are the same, pixel for pixel, and the picture is the one the shape says it is.
//
// Three things are asserted at every fraction, and they are what "the same picture" means:
//
//   - the two buffers are equal, entry by entry, so no pixel is described one way and drawn
//     another;
//   - the number of pixels in the frame's colour is the area of the fill rectangle the body
//     names, and every one of them lies inside the columns the fill covers;
//   - every pixel in the remainder is the unfilled colour, so nothing is left showing the
//     frame before this one.
//
// At 0 % and 100 % there is no remainder to check: the body is one rectangle over the whole
// panel with the remainder segment switched off, and the assertions that remain are that the
// whole panel is the frame's colour and that both paths agree. Those two are the edges where a
// bare "fill area" test would be vacuous, which is why they are stated separately.
func TestTheJSONBodyAndTheDDPPixelsAreTheSamePicture(t *testing.T) {
	at := time.Now()

	for _, percent := range percentages {
		t.Run(fmt.Sprintf("%g percent", percent), func(t *testing.T) {
			frame := render.Decide(printingAt(percent, at), at, render.Config{LEDs: livePanel.width * livePanel.height})
			geometry := matrix.Geometry{Width: livePanel.width, Height: livePanel.height}

			body := bodyFor(t, frame, livePanel)
			fromJSON := jsonPicture(t, body, livePanel)

			fromDDP, err := matrix.Raster(frame, geometry)
			if err != nil {
				t.Fatalf("the pixel path refused the frame: %v", err)
			}

			if len(fromJSON) != len(fromDDP) {
				t.Fatalf("the body describes %d pixels and the pixel path %d", len(fromJSON), len(fromDDP))
			}

			// One picture, entry by entry. This is the whole test: a run that agrees here has
			// no pixel described one way and drawn another.
			for index := range fromDDP {
				if fromDDP[index] != fromJSON[index] {
					t.Fatalf("pixel (%d,%d) is %+v over DDP and %+v in the JSON body",
						index%geometry.Width, index/geometry.Width, fromDDP[index], fromJSON[index])
				}
			}

			// The boundary the module's own painter computes, which is the one the body's fill
			// rectangle covers.
			columns := fillColumns(frame, livePanel)

			lit, dark := 0, 0

			for index, colour := range fromDDP {
				column := index % geometry.Width

				switch colour {
				case frame.Colour:
					lit++ // the completed part of the job, or the whole panel at an edge
				case frame.Unfilled():
					dark++ // the remainder, or nothing at an edge
				default:
					t.Fatalf("pixel (%d,%d) is %+v, which is neither the frame's colour nor the unfilled one",
						column, index/geometry.Width, colour)
				}

				// Every pixel in the frame's colour lies inside the columns the fill covers.
				if colour == frame.Colour && columns > 0 && columns < geometry.Width && column >= columns {
					t.Fatalf("pixel (%d,%d) is the frame's colour, outside the fill's columns 0..%d",
						column, index/geometry.Width, columns)
				}

				// Every pixel past the boundary is the unfilled colour, which on this panel is
				// dark: the remainder must not keep anything from the frame before it.
				if columns > 0 && columns < geometry.Width && column >= columns && colour != frame.Unfilled() {
					t.Fatalf("pixel (%d,%d) is in the remainder and is %+v, want the unfilled colour",
						column, index/geometry.Width, colour)
				}
			}

			if columns > 0 && columns < geometry.Width {
				// A bar: its area is the fill's columns over every row, and the two counts
				// account for every pixel of the panel exactly once.
				if want := columns * livePanel.height; lit != want {
					t.Fatalf("%d pixels are the frame's colour, want %d: %d columns over %d rows",
						lit, want, columns, livePanel.height)
				}

				if want := (livePanel.width - columns) * livePanel.height; dark != want {
					t.Fatalf("%d pixels are in the remainder, want %d", dark, want)
				}

				// The body is two rectangles that tile the panel: adjacent, and together the
				// whole width. A gap leaves pixels to whatever was under them and a double
				// cover puts one rectangle over the other, and both look like a bar.
				if len(body.Segments) != 2 {
					t.Fatalf("the body carries %d segments, want two", len(body.Segments))
				}

				fill, rest := body.Segments[0], body.Segments[1]

				if fill.Start == nil || rest.Start == nil ||
					*fill.Start != 0 || *fill.Stop != columns ||
					*rest.Start != columns || *rest.Stop != livePanel.width {
					t.Fatalf("the body's rectangles are %v..%v and %v..%v, want 0..%d and %d..%d",
						fill.Start, fill.Stop, rest.Start, rest.Stop, columns, columns, livePanel.width)
				}

				if fill.StartY == nil || fill.StopY == nil || *fill.StartY != 0 || *fill.StopY != livePanel.height {
					t.Fatalf("the fill covers rows %v..%v, want 0..%d", fill.StartY, fill.StopY, livePanel.height)
				}
			} else {
				// An edge: the whole panel in the frame's colour, and no remainder at all.
				if lit != geometry.Pixels() || dark != 0 {
					t.Fatalf("%d pixels are the frame's colour and %d are not, want all %d and none",
						lit, dark, geometry.Pixels())
				}

				if len(body.Segments) != 2 {
					t.Fatalf("the body carries %d segments, want the fill and the switched-off remainder", len(body.Segments))
				}

				if on := body.Segments[1].On; on == nil || *on {
					t.Fatalf("the remainder segment is %v, want it switched off", on)
				}

				// Where a segment is off it names no colour and no rectangle: that is what keeps
				// a stale bar out of a frame that has none.
				if body.Segments[1].Start != nil || len(body.Segments[1].Colour) != 0 {
					t.Fatalf("the switched-off remainder names %+v, want no bounds and no colour", body.Segments[1])
				}
			}

			t.Logf("%g %%: %d of %d lit over %d columns, %d in the remainder of %dx%d (%d pixels of colour over DDP)",
				percent, lit, livePanel.width*livePanel.height, columns, dark,
				livePanel.width, livePanel.height, len(fromDDP))
		})
	}
}
