package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProtocolV2WireShape(t *testing.T) {
	h := fixture()
	tick(t, h)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(h.requests[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	keys := []string{"version", "instance_id", "sequence", "snapshot_at", "selection_status", "matching_stream_count", "selected_stream", "filters"}
	if len(wire) != len(keys) {
		t.Fatal("unexpected outbound fields", wire)
	}
	for _, key := range keys {
		if _, ok := wire[key]; !ok {
			t.Fatal("missing wire field", key)
		}
	}
	put(t, h, report(h, "stable-client", "stopped", "song"))
	tick(t, h)
	if err := json.Unmarshal(h.requests[1].Body, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["selected_stream"]) != "null" || string(wire["matching_stream_count"]) != "0" || string(wire["selection_status"]) != `"no_match"` {
		t.Fatal("invalid empty selection", wire)
	}
}
func TestExpiryFallsBackToMostRecentPaused(t *testing.T) {
	h := emptyFixture()
	paused := report(h, "paused", "paused", "paused-track")
	put(t, h, paused)
	playing := report(h, "active", "playing", "active-track")
	playing.Track.Duration = 0
	playing.PositionMs = 0
	put(t, h, playing)
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "active" || latest(t, h).Count != 2 {
		t.Fatal(latest(t, h))
	}
	h.now = h.now.Add(5 * time.Second)
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "paused" || latest(t, h).Count != 1 {
		t.Fatal("expired active didn't fall back", latest(t, h))
	}
	// The fallback keeps the original sample time and position, not collection time.
	if latest(t, h).At != time.Unix(paused.Timestamp, 0).UTC().Format(time.RFC3339) || latest(t, h).Selected["positionMs"] != float64(paused.PositionMs) {
		t.Fatal("fallback sample was rebased")
	}
	h.now = time.Unix(paused.Timestamp+1800, 0)
	tick(t, h)
	if latest(t, h).Selected != nil {
		t.Fatal("paused fallback failed to expire")
	}
	for _, uri := range h.uris {
		if len(uri) < 8 || uri[:8] != "getSong?" {
			t.Fatal("selection polled playback", uri)
		}
	}
}
func TestKVReadRaceNeverLosesRefreshToken(t *testing.T) {
	// GetMany supplies a point-in-time batch, while the report arriving during
	// artwork resolution belongs to the next collection (not the current send).
	h := fixture()
	once := true
	h.onJSON = func() {
		if once {
			once = false
			put(t, h, report(h, "new-client", "starting", "new-track"))
		}
	}
	tick(t, h)
	if latest(t, h).Selected["id"] != "song" {
		t.Fatal("in-flight sample changed")
	}
	h.onJSON = nil
	tick(t, h)
	if latest(t, h).Selected["id"] != "new-track" || latest(t, h).Count != 2 {
		t.Fatal("refresh during read lost", latest(t, h))
	}
}
