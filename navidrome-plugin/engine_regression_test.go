package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPrefixedReceiverURL(t *testing.T) {
	for _, prefix := range []string{"", "/prefix", "/prefix/deeper"} {
		base := "https://ha" + prefix + "/api/navidrome_nowplaying/UUID"
		if !config(base, "secret") {
			t.Fatal(base)
		}
		for _, suffix := range []string{"/", "/other", "?", "?x=y", "#", "#fragment", "%2Fother"} {
			if config(base+suffix, "secret") {
				t.Fatal(base + suffix)
			}
		}
	}
}
func TestAcknowledgementDifferentInstanceSameSequence(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		h := fixture()
		h.send = func(r Request) (*Response, error) {
			s := snap(t, r)
			return &Response{Status: 200, Body: []byte(fmt.Sprintf(`{"accepted":%t,"instance_id":"different","accepted_sequence":%d}`, accepted, s.Sequence))}, nil
		}
		tick(t, h)
		first := latest(t, h)
		h.send = nil
		h.now = h.now.Add(time.Second)
		tick(t, h)
		next := latest(t, h)
		if next.Instance == first.Instance || next.Sequence != 1 {
			t.Fatal(first, next)
		}
	}
}

type fakeHA struct {
	current Snapshot
	body    []byte
	lost    bool
}

func (ha *fakeHA) send(r Request) (*Response, error) {
	var s Snapshot
	if err := json.Unmarshal(r.Body, &s); err != nil {
		return nil, err
	}
	accepted := false
	if ha.current.Instance == "" || (s.Instance != ha.current.Instance && s.Sequence == 1) || (s.Instance == ha.current.Instance && s.Sequence > ha.current.Sequence) {
		ha.current = s
		ha.body = append([]byte(nil), r.Body...)
		accepted = true
	} else if s.Instance == ha.current.Instance && s.Sequence == ha.current.Sequence {
		accepted = bytes.Equal(ha.body, r.Body)
	}
	if !ha.lost {
		ha.lost = true
		return nil, errors.New("accepted but response lost")
	}
	b, _ := json.Marshal(map[string]any{"accepted": accepted, "instance_id": ha.current.Instance, "accepted_sequence": ha.current.Sequence, "missing_artwork": []string{}})
	return &Response{Status: 200, Body: b}, nil
}
func TestAcceptedResponseLostThenStop(t *testing.T) {
	h := fixture()
	ha := &fakeHA{}
	h.send = ha.send
	tick(t, h)
	first := append([]byte(nil), h.requests[0].Body...)
	for _, state := range []string{"playing", "stopped"} {
		put(t, h, report(h, "stable-client", state, "intermediate"))
		tick(t, h)
	}
	if len(h.requests) != 1 {
		t.Fatal("backoff bypassed")
	}
	h.now = h.now.Add(2 * time.Second)
	tick(t, h)
	if len(h.requests) != 3 || !bytes.Equal(first, h.requests[1].Body) || ha.current.Sequence != 2 || ha.current.Selected != nil {
		t.Fatal("retry mutated or stop lost", ha.current)
	}
	stop := latest(t, h)
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	heartbeat := latest(t, h)
	if heartbeat.At != stop.At || heartbeat.Sequence != 3 || heartbeat.Selected != nil {
		t.Fatal(heartbeat)
	}
	var st session
	_ = json.Unmarshal(h.cache["session"], &st)
	if st.Pending != nil || st.Newest || len(h.cache) != 3 {
		t.Fatal("unbounded state")
	}
}
func TestHAEmptyRestartAdoptsPositiveSequence(t *testing.T) {
	h := fixture()
	ha := &fakeHA{lost: true}
	h.send = ha.send
	tick(t, h)
	first := ha.current
	ha.current = Snapshot{}
	ha.body = nil
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	if ha.current.Instance != first.Instance || ha.current.Sequence != 2 {
		t.Fatal(ha.current)
	}
}

func TestSameUserHandoffAndDelayedOldEvents(t *testing.T) {
	for _, oldState := range []string{"paused", "playing"} {
		for _, newState := range []string{"playing", "starting"} {
			t.Run(oldState+"-"+newState, func(t *testing.T) {
				h := emptyFixture()
				old := report(h, "old-player", oldState, "old-track")
				put(t, h, old)
				h.now = h.now.Add(time.Second)
				fresh := report(h, "new-player", newState, "new-track")
				put(t, h, fresh)
				tick(t, h)
				if latest(t, h).Selected["playerId"] != "new-player" || latest(t, h).Selected["id"] != "new-track" || latest(t, h).Count != 2 {
					t.Fatal("handoff ambiguous", latest(t, h))
				}
				for _, state := range []string{"paused", "stopped", "expired"} {
					h.now = h.now.Add(time.Second)
					old.State = state
					old.Timestamp = h.now.Unix()
					put(t, h, old)
					tick(t, h)
					if latest(t, h).Selected["playerId"] != "new-player" {
						t.Fatal("old event cleared new player", state, latest(t, h))
					}
				}
			})
		}
	}
}
func TestSameNameDistinctIDsLatestServerTimestamp(t *testing.T) {
	h := emptyFixture()
	a := report(h, "stable-A", "playing", "A")
	b := report(h, "stable-B", "starting", "B")
	a.Timestamp += 2
	put(t, h, a)
	put(t, h, b)
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "stable-A" || latest(t, h).Count != 2 || len(h.kv) != 2 {
		t.Fatal("receipt overrode server timestamp or playerName collision", latest(t, h))
	}
	if strings.Contains(string(h.requests[0].Body), `"streams"`) {
		t.Fatal("all streams leaked")
	}
}
func TestOlderSamePlayerIgnoredAndTombstonePreventsResurrection(t *testing.T) {
	h := fixture()
	h.now = h.now.Add(2 * time.Second)
	stop := report(h, "stable-client", "stopped", "song")
	put(t, h, stop)
	tick(t, h)
	before := h.kvWrites
	old := stop
	old.State = "playing"
	old.Timestamp--
	put(t, h, old)
	tick(t, h)
	if h.kvWrites != before || latest(t, h).Selected != nil {
		t.Fatal("old playing resurrected terminal record")
	}
	key := playerKey(stop.Username, stop.PlayerId)
	var stored playerRecord
	_ = json.Unmarshal(h.kv[key], &stored)
	if stored.Report.State != "stopped" || h.ttl[key]-h.now.Unix() < tombstoneSeconds {
		t.Fatal("tombstone not retained")
	}
}
func TestSameSecondOrderingWithFixedClock(t *testing.T) {
	h := emptyFixture()
	put(t, h, report(h, "A", "playing", "A"))
	put(t, h, report(h, "B", "starting", "B"))
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "B" {
		t.Fatal("same second callbacks not ordered")
	}
	put(t, h, report(h, "A", "playing", "new-A"))
	tick(t, h)
	if latest(t, h).Selected["id"] != "new-A" {
		t.Fatal("per-player same second dropped")
	}
}
func TestExpiryEventFreePlayingAndPaused(t *testing.T) {
	for _, state := range []string{"playing", "starting", "paused"} {
		t.Run(state, func(t *testing.T) {
			h := emptyFixture()
			r := report(h, "client", state, "song")
			r.Track.Duration = 10
			r.PositionMs = 4000
			r.PlaybackRate = 2
			put(t, h, r)
			tick(t, h)
			seconds := int64(8)
			if state == "paused" {
				seconds = 1800
			}
			h.now = h.now.Add(time.Duration(seconds-1) * time.Second)
			tick(t, h)
			if latest(t, h).Selected == nil {
				t.Fatal("expired early")
			}
			h.now = h.now.Add(time.Second)
			tick(t, h)
			if latest(t, h).Selected != nil || latest(t, h).Status != "no_match" || latest(t, h).Count != 0 {
				t.Fatal("expiry did not clear", latest(t, h))
			}
			if h.jsonCalls != 1 || h.kvWrites != 1 {
				t.Fatal("expiry polled playback or modified records")
			}
		})
	}
}
func TestDelayedPlayingUsesSampleTimeAndDefaultRate(t *testing.T) {
	h := emptyFixture()
	r := report(h, "client", "playing", "song")
	r.PlaybackRate = 0
	r.Track.Duration = 5
	r.PositionMs = 0
	r.Timestamp -= 11
	put(t, h, r)
	tick(t, h)
	if latest(t, h).Selected != nil {
		t.Fatal("receipt restarted timeout")
	}
	r.Timestamp = h.now.Unix()
	put(t, h, r)
	tick(t, h)
	if latest(t, h).Selected["playbackRate"] != float64(1) {
		t.Fatal("default rate")
	}
	h.now = h.now.Add(10 * time.Second)
	tick(t, h)
	if latest(t, h).Selected != nil {
		t.Fatal("default rate timeout")
	}
}
func TestPausedFallbackAndCrossUserAmbiguity(t *testing.T) {
	h := emptyFixture()
	h.users = []string{"alice", "bob"}
	a := report(h, "a", "paused", "a")
	b := report(h, "b", "paused", "b")
	put(t, h, a)
	put(t, h, b)
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "b" {
		t.Fatal("most recent paused not selected")
	}
	bob := report(h, "c", "paused", "c")
	bob.Username = "bob"
	put(t, h, bob)
	tick(t, h)
	if latest(t, h).Status != "ambiguous" || latest(t, h).Selected != nil || latest(t, h).Count != 3 {
		t.Fatal(latest(t, h))
	}
	a.State = "playing"
	put(t, h, a)
	tick(t, h)
	if latest(t, h).Selected["playerId"] != "a" {
		t.Fatal("paused user overrode active user")
	}
	bob.State = "starting"
	put(t, h, bob)
	tick(t, h)
	if latest(t, h).Status != "ambiguous" || latest(t, h).Selected != nil {
		t.Fatal("multiple active users guessed")
	}
}
func TestSourceFiltersANDAndAuthorizationChanges(t *testing.T) {
	h := emptyFixture()
	h.users = []string{"alice", "bob"}
	a := report(h, "A", "playing", "A")
	a.PlayerName = "Kitchen"
	put(t, h, a)
	b := report(h, "B", "playing", "B")
	b.PlayerName = "Bedroom"
	put(t, h, b)
	bob := a
	bob.Username = "bob"
	put(t, h, bob)
	h.username = "alice"
	h.playerName = "Kitchen"
	tick(t, h)
	if latest(t, h).Count != 1 || latest(t, h).Selected["id"] != "A" {
		t.Fatal("filters not AND", latest(t, h))
	}
	h.playerName = "kitchen"
	tick(t, h)
	if latest(t, h).Status != "no_match" {
		t.Fatal("name filter not exact")
	}
	h.username = ""
	h.playerName = "Kitchen"
	tick(t, h)
	if latest(t, h).Status != "ambiguous" {
		t.Fatal("name ignored user ambiguity")
	}
	h.users = []string{"alice"}
	tick(t, h)
	if latest(t, h).Count != 1 || latest(t, h).Selected["username"] != "alice" {
		t.Fatal("stored removed ACL leaked")
	}
	writes := h.kvWrites
	put(t, h, bob)
	if writes != h.kvWrites {
		t.Fatal("removed assignment authorized report")
	}
	h.users = nil
	tick(t, h)
	if latest(t, h).Selected != nil || latest(t, h).Count != 0 {
		t.Fatal("ACL removal retained selection")
	}
}
func TestOversizedCollectionBlocksExistingHeartbeat(t *testing.T) {
	h := emptyFixture()
	r := report(h, "stable-client", "playing", "song")
	r.Track.Duration = 3600
	put(t, h, r)
	tick(t, h)
	for i := 0; i < 128; i++ {
		put(t, h, report(h, fmt.Sprint(i), "paused", "song"))
	}
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(90 * time.Second)
		tick(t, h)
	}
	if len(h.requests) != 1 {
		t.Fatal("oversized snapshot truncated or heartbeated")
	}
}
func TestKVFailuresBlockPartialSelection(t *testing.T) {
	h := fixture()
	tick(t, h)
	h.kvError = errors.New("KV unavailable")
	h.now = h.now.Add(130 * time.Second)
	tick(t, h)
	if len(h.requests) != 1 {
		t.Fatal("KV failure emitted old selection")
	}
	h.kvError = nil
	h.now = h.now.Add(10 * time.Second)
	tick(t, h)
	if latest(t, h).Selected != nil {
		t.Fatal("KV recovery not expired")
	}
}
func TestSanitizedMetadataPrivacyAndOpaqueArt(t *testing.T) {
	h := emptyFixture()
	r := report(h, "player /?&", "playing", "track /?&")
	r.Track.Title = "Exact callback"
	put(t, h, r)
	tick(t, h)
	s := latest(t, h)
	if s.Selected["title"] != "Exact callback" || s.Selected["coverArt"] != "al-1" || s.Selected["playerId"] != r.PlayerId {
		t.Fatal(s)
	}
	if h.uris[0] != "getSong?u=alice&id=track+%2F%3F%26" {
		t.Fatal("getSong not exact encoded ID", h.uris)
	}
	for _, b := range append([][]byte{h.requests[0].Body, h.cache["session"]}, h.kv[playerKey(r.Username, r.PlayerId)]) {
		for _, secret := range []string{"private/path", "Stale API title", "path\"", "mbz", "libraryId"} {
			if strings.Contains(string(b), secret) {
				t.Fatal("private metadata persisted", secret)
			}
		}
	}
}
func TestInvalidSamplesAndFinitePositiveTTL(t *testing.T) {
	for _, alter := range []func(*PlaybackReport){func(r *PlaybackReport) { r.State = "idle" }, func(r *PlaybackReport) { r.Timestamp = 0 }, func(r *PlaybackReport) { r.PositionMs = -1 }, func(r *PlaybackReport) { r.PlaybackRate = math.NaN() }, func(r *PlaybackReport) { r.Track.Duration = math.Inf(1) }, func(r *PlaybackReport) { r.Timestamp = math.MaxInt64 }} {
		h := emptyFixture()
		r := report(h, "client", "playing", "song")
		alter(&r)
		if err := (Engine{h}).Report(r); err == nil {
			t.Fatal("invalid sample accepted")
		}
	}
	h := emptyFixture()
	r := report(h, "client", "playing", "song")
	r.PlaybackRate = math.SmallestNonzeroFloat64
	put(t, h, r)
	for _, expiry := range h.ttl {
		if expiry <= h.now.Unix() || expiry-h.now.Unix() > maxPlaybackSeconds+tombstoneSeconds {
			t.Fatal("unbounded TTL")
		}
	}
}
