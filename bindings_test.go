package main

import (
	"strings"
	"testing"
)

// wholeStrip is the segment a binding names to drive a controller nobody else uses.
const wholeStrip = -1

// TestBindingsHoldTheOperatorsRules is the rules of 2026-11-18 written as cases: a set the module
// can run is accepted, and a set it cannot is refused with a sentence naming what collided.
//
// The cases are here rather than spread through a watch test because these are the rules, and a
// watch that started on a bad set would be the failure the rules exist to prevent — two writers
// on one segment, or two displays on one printer, undoing each other at the rate of a tick.
func TestBindingsHoldTheOperatorsRules(t *testing.T) {
	tests := []struct {
		name string
		set  bindings
		want string // a substring of the refusal, empty for a set that runs
	}{
		{
			name: "one printer on the whole of one controller",
			set:  bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128}},
		},
		{
			name: "three printers, three segments of one controller",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
				{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 5},
				{DeviceID: "printer-c", Address: "http://10.0.0.136", Segment: 7},
			},
		},
		{
			name: "two printers, two controllers",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
				{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 2},
			},
		},
		{
			name: "nothing to watch",
			set:  bindings{},
			want: "nothing to watch",
		},
		{
			// docs/product-requirements.md §2.2: "A printer may sit on several lights — multi-room
			// status — at the cost of a second steady stream." Two bindings, one printer, two
			// controllers, both whole: accepted.
			name: "one printer on two lights",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
				{DeviceID: "printer-a", Address: "http://10.0.0.253", Segment: wholeStrip, Brightness: 96},
			},
		},
		{
			// And the same printer on two segments of one controller: the segment is the unit, so
			// this is two zones of one display, which the rules allow.
			name: "one printer on two segments of one controller",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 5},
			},
		},
		{
			// The rule multi-room does not lift: a segment carries one printer, whoever's.
			name: "one printer twice on one segment",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
			},
			want: "one segment is shown by one binding",
		},
		{
			name: "two bindings sharing one name",
			set: bindings{
				{ID: "same", DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
				{ID: "same", DeviceID: "printer-a", Address: "http://10.0.0.253", Segment: 2},
			},
			want: "two bindings cannot share one",
		},
		{
			name: "one segment in two bindings, one with a trailing slash",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
				{DeviceID: "printer-b", Address: "http://10.0.0.136/", Segment: 2},
			},
			want: "one segment is shown by one binding",
		},
		{
			name: "one segment in two bindings, the host written in capitals",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://wled.lan", Segment: 4},
				{DeviceID: "printer-b", Address: "HTTP://WLED.LAN", Segment: 4},
			},
			want: "one segment is shown by one binding",
		},
		{
			name: "the whole controller beside a segment of it",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
				{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 5},
			},
			want: "a whole controller leaves no segment for anything else",
		},
		{
			name: "a segment beside the whole controller, written the other way round",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 5},
				{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
			},
			want: "a whole controller leaves no segment for anything else",
		},
		{
			name: "the whole of one controller in two bindings",
			set: bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
				{DeviceID: "printer-b", Address: "http://10.0.0.136/", Segment: wholeStrip, Brightness: 128},
			},
			want: "already drives",
		},
		{
			name: "a segment number below minus one",
			set:  bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -2}},
			want: "name one segment by its number",
		},
		{
			name: "a brightness no controller takes",
			set:  bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 300}},
			want: "takes 0-255",
		},
		{
			name: "a brightness on a borrowed segment",
			set:  bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2, Brightness: 128}},
			want: "a brightness is controller-wide",
		},
		{
			name: "an address with no scheme",
			set:  bindings{{DeviceID: "printer-a", Address: "10.0.0.136", Segment: wholeStrip, Brightness: 128}},
			want: "needs an http or https scheme",
		},
		{
			name: "a binding that names no printer",
			set:  bindings{{DeviceID: "  ", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128}},
			want: "names no printer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.set.check()

			if test.want == "" {
				if err != nil {
					t.Fatalf("the set was refused: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("the set was accepted, want a refusal containing %q", test.want)
			}

			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("the refusal reads %q, want it to contain %q", err, test.want)
			}
		})
	}
}

// TestControllersGroupTheBindingsOfOneAddress is the grouping a watch is built from: one entry
// per controller, its bindings in the operator's order, and two spellings of one address in one
// entry rather than two.
func TestControllersGroupTheBindingsOfOneAddress(t *testing.T) {
	set := bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: wholeStrip, Brightness: 64},
		{DeviceID: "printer-c", Address: "http://10.0.0.136/", Segment: 5},
	}

	grouped, err := set.controllers()
	if err != nil {
		t.Fatalf("controllers: %v", err)
	}

	if len(grouped) != 2 {
		t.Fatalf("the set was grouped into %d controllers, want 2", len(grouped))
	}

	if grouped[0].Address != "http://10.0.0.136" {
		t.Fatalf("the first controller is %q, want the address as the operator first wrote it", grouped[0].Address)
	}

	if len(grouped[0].Bindings) != 2 {
		t.Fatalf("the panel carries %d bindings, want 2", len(grouped[0].Bindings))
	}

	if grouped[0].Bindings[0].DeviceID != "printer-a" || grouped[0].Bindings[1].DeviceID != "printer-c" {
		t.Fatalf("the bindings are %q then %q, want printer-a then printer-c",
			grouped[0].Bindings[0].DeviceID, grouped[0].Bindings[1].DeviceID)
	}

	if len(grouped[1].Bindings) != 1 || grouped[1].Bindings[0].DeviceID != "printer-b" {
		t.Fatalf("the second controller does not carry the one binding written for it")
	}
}

// TestControllersRefuseWhatCheckRefuses is the other half of the grouping's contract: a set that
// breaks a rule is refused before anything is built, so no client is ever made for it.
func TestControllersRefuseWhatCheckRefuses(t *testing.T) {
	_, err := (bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
		{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 2},
	}).controllers()

	if err == nil {
		t.Fatal("a set with two bindings on one segment was grouped, want a refusal")
	}
}
