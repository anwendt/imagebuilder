package builder_test

import (
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/pkg/builder"
)

func TestParseBootCommand_ParsesTextKeysAndWaits(t *testing.T) {
	events, err := builder.ParseBootCommand([]string{"<tab>", " inst.ks=http://example.test/ks.cfg", "<enter><wait5>"})
	if err != nil {
		t.Fatalf("ParseBootCommand returned error: %v", err)
	}
	want := []builder.BootEvent{
		{Type: builder.BootEventKey, Value: "tab"},
		{Type: builder.BootEventText, Value: " inst.ks=http://example.test/ks.cfg"},
		{Type: builder.BootEventKey, Value: "enter"},
		{Type: builder.BootEventWait, Wait: 5 * time.Second},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event[%d] = %#v, want %#v", i, events[i], want[i])
		}
	}
}

func TestParseBootCommand_RejectsUnknownToken(t *testing.T) {
	_, err := builder.ParseBootCommand([]string{"<poweroff>"})
	if err == nil {
		t.Fatal("ParseBootCommand should reject unknown tokens")
	}
}

func TestBootEventsToQEMUMonitorScript(t *testing.T) {
	events := []builder.BootEvent{
		{Type: builder.BootEventKey, Value: "tab"},
		{Type: builder.BootEventText, Value: " a"},
		{Type: builder.BootEventKey, Value: "enter"},
	}
	got := builder.BootEventsToQEMUMonitorScript(events)
	want := []string{"sendkey tab", "sendkey spc", "sendkey a", "sendkey ret"}
	if len(got) != len(want) {
		t.Fatalf("script = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("script[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
