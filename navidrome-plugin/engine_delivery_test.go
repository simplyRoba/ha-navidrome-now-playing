package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestArtworkFailureDoesNotBlockMetadataOrHeartbeat(t *testing.T) {
	h := fixture()
	tick(t, h)
	r := report(h, "stable-client", "playing", "new")
	r.Track.Duration = 3600
	put(t, h, r)
	h.jsonError = errors.New("unavailable")
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	if len(h.requests) != 3 || latest(t, h).Selected["id"] != "new" || latest(t, h).Selected["title"] != "Callback title" {
		t.Fatal("artwork failure blocked authoritative metadata or heartbeat")
	}
	if _, ok := latest(t, h).Selected["coverArt"]; ok {
		t.Fatal("failed artwork enrichment retained stale art")
	}
	// Stops don't depend on artwork API availability, so they clear immediately.
	put(t, h, report(h, "stable-client", "stopped", "new"))
	tick(t, h)
	if latest(t, h).Sequence != 4 || latest(t, h).Selected != nil {
		t.Fatal(latest(t, h))
	}
}
func TestReportDuringCollectionRemainsPending(t *testing.T) {
	h := fixture()
	h.onJSON = func() { put(t, h, report(h, "stable-client", "stopped", "song")) }
	tick(t, h)
	h.onJSON = nil
	h.now = h.now.Add(time.Second)
	tick(t, h)
	if h.jsonCalls != 1 || latest(t, h).Selected != nil || latest(t, h).Sequence != 2 {
		t.Fatal("report during collect lost", latest(t, h))
	}
}
func TestPluginRestartStartsNewSessionAndRestoresKV(t *testing.T) {
	h := fixture()
	e := Engine{h}
	if err := e.Init(); err != nil {
		t.Fatal(err)
	}
	tick(t, h)
	first := latest(t, h)
	h.schedules = map[string]string{}
	if err := e.Init(); err != nil {
		t.Fatal(err)
	}
	tick(t, h)
	second := latest(t, h)
	if second.Instance == first.Instance || second.Sequence != 1 || second.Selected["id"] != "song" {
		t.Fatal(first, second)
	}
	h.cache = map[string][]byte{}
	h.schedules = map[string]string{}
	if err := e.Init(); err != nil {
		t.Fatal(err)
	}
	tick(t, h)
	third := latest(t, h)
	if third.Instance == second.Instance || third.Sequence != 1 || third.Selected["playerId"] != "stable-client" {
		t.Fatal(third)
	}
	if h.kvWrites != 1 {
		t.Fatal("scheduler wrote callback records")
	}
}
func TestBadAcknowledgementsKeepSequenceAndBoundBackoff(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{302, `{"accepted_sequence":1}`}, {401, `{}`}, {500, `{}`}, {200, `{}`}, {200, `{"accepted_sequence":"1"}`}, {200, `{"accepted_sequence":9}`}, {200, `not json`}, {200, strings.Repeat("x", maxSnapshot+1)}} {
		t.Run(fmt.Sprint(tc.status, len(tc.body)), func(t *testing.T) {
			h := fixture()
			h.send = func(Request) (*Response, error) { return &Response{Status: tc.status, Body: []byte(tc.body)}, nil }
			for i := 0; i < 10; i++ {
				tick(t, h)
				var st session
				if err := json.Unmarshal(h.cache["session"], &st); err != nil {
					t.Fatal(err)
				}
				if st.Sequence != 1 || st.Next-h.now.Unix() > 32 {
					t.Fatal(st)
				}
				h.now = time.Unix(st.Next, 0)
			}
			if len(h.kv) != 1 || len(h.cache) != 3 {
				t.Fatal("retry queued state")
			}
			h.send = nil
			tick(t, h)
			if snap(t, h.requests[len(h.requests)-1]).Sequence < 1 {
				t.Fatal("skipped sequence")
			}
			if string(h.requests[0].Body) != string(h.requests[1].Body) {
				t.Fatal("retry changed")
			}
		})
	}
}
func TestSubsonicArtworkErrorsStillPublishCallbackMetadata(t *testing.T) {
	for _, raw := range []string{`{"subsonic-response":{"status":"failed","error":{"code":70,"message":"private server detail"}}}`, `not json`, `{"status":"ok","song":"bad"}`, `{"status":"ok","song":{"id":"other","coverArt":"wrong-art"}}`, `{"status":"ok"}`, `{"status":"ok","song":{"id":"song"}}`} {
		h := fixture()
		h.raw = raw
		tick(t, h)
		if len(h.requests) != 1 || latest(t, h).Status != "selected" || latest(t, h).Selected["id"] != "song" || latest(t, h).Selected["title"] != "Callback title" {
			t.Fatal("optional artwork blocked callback metadata", raw)
		}
		if _, ok := latest(t, h).Selected["coverArt"]; ok {
			t.Fatal("invalid artwork published", raw)
		}
		if strings.Contains(string(h.requests[0].Body), "private server detail") {
			t.Fatal("API error leaked")
		}
		h.now = h.now.Add(90 * time.Second)
		tick(t, h)
		if len(h.requests) != 2 || latest(t, h).Sequence != 2 {
			t.Fatal("artwork error blocked heartbeat")
		}
	}
}
func TestSelectedOnlyArtworkBudget(t *testing.T) {
	h := fixture()
	put(t, h, report(h, "old", "paused", "private-track"))
	h.send = func(r Request) (*Response, error) {
		if strings.HasSuffix(r.URL, "/artwork") {
			return nil, errors.New("offline art")
		}
		return ack(snap(t, r), []string{"unknown", "private-track", "song", "al-1", "al-1"}), nil
	}
	tick(t, h)
	if len(h.requests) != 2 || h.requests[1].Headers["X-Navidrome-Artwork-ID"] != "al-1" {
		t.Fatal("only selected art allowed")
	}
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	if snap(t, h.requests[2]).Sequence != 2 {
		t.Fatal("art blocked metadata ACK")
	}
}
