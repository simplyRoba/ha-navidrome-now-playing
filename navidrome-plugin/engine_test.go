package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeHost struct {
	cache, kv                                    map[string][]byte
	ttl                                          map[string]int64
	schedules                                    map[string]string
	scheduleTimes                                map[string]time.Time
	scheduleCalls                                []scheduleRequest
	users                                        []string
	now                                          time.Time
	id                                           int
	url, secret, raw, mime, username, playerName string
	image                                        []byte
	requests                                     []Request
	uris                                         []string
	jsonCalls, kvWrites                          int
	send                                         func(Request) (*Response, error)
	jsonError, rawError, kvError, usersError     error
	onJSON                                       func()
}

func fixture() *fakeHost {
	h := emptyFixture()
	r := report(h, "stable-client", "playing", "song")
	if err := (Engine{h}).Report(r); err != nil {
		panic(err)
	}
	return h
}
func emptyFixture() *fakeHost {
	return &fakeHost{cache: map[string][]byte{}, kv: map[string][]byte{}, ttl: map[string]int64{}, schedules: map[string]string{}, users: []string{"alice"}, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), url: "http://ha:8123/api/navidrome_nowplaying/receiver", secret: "test-secret", mime: "image/png", image: []byte("\x89PNG\r\n\x1a\nbody")}
}
func report(h *fakeHost, player, state, track string) PlaybackReport {
	return PlaybackReport{Username: "alice", Track: TrackInfo{ID: track, Title: "Callback title", Artist: "Artist", Album: "Album", Duration: 180.7}, State: state, PositionMs: 1234, PlaybackRate: 1.5, PlayerId: player, PlayerName: "Client", Timestamp: h.now.Unix()}
}
func put(t *testing.T, h *fakeHost, r PlaybackReport) {
	t.Helper()
	if err := (Engine{h}).Report(r); err != nil {
		t.Fatal(err)
	}
}
func (h *fakeHost) Config() Config {
	return Config{URL: h.url, Secret: h.secret, Username: h.username, PlayerName: h.playerName}
}
func (h *fakeHost) Users() ([]string, error)           { return append([]string(nil), h.users...), h.usersError }
func (h *fakeHost) Get(k string) ([]byte, bool, error) { b, ok := h.cache[k]; return b, ok, nil }
func (h *fakeHost) Set(k string, b []byte) error       { h.cache[k] = append([]byte(nil), b...); return nil }
func (h *fakeHost) KVGet(k string) ([]byte, bool, error) {
	b, ok := h.kv[k]
	if h.ttl[k] <= h.now.Unix() {
		ok = false
	}
	return b, ok, h.kvError
}
func (h *fakeHost) KVSetWithTTL(k string, b []byte, ttl int64) error {
	if h.kvError != nil {
		return h.kvError
	}
	h.kvWrites++
	h.kv[k] = append([]byte(nil), b...)
	h.ttl[k] = h.now.Unix() + ttl
	return nil
}
func (h *fakeHost) KVList(prefix string) ([]string, error) {
	var keys []string
	for k := range h.kv {
		if strings.HasPrefix(k, prefix) && h.ttl[k] > h.now.Unix() {
			keys = append(keys, k)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, h.kvError
}
func (h *fakeHost) KVGetMany(keys []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, k := range keys {
		b, ok, _ := h.KVGet(k)
		if ok {
			out[k] = append([]byte(nil), b...)
		}
	}
	return out, h.kvError
}
func (h *fakeHost) Schedule(id, p string, delay int32) error {
	if _, ok := h.schedules[id]; ok {
		return fmt.Errorf("schedule ID %q already exists", id)
	}
	h.schedules[id] = p
	if h.scheduleTimes == nil {
		h.scheduleTimes = map[string]time.Time{}
	}
	h.scheduleTimes[id] = h.now.Add(time.Duration(delay) * time.Second)
	h.scheduleCalls = append(h.scheduleCalls, scheduleRequest{id, p, delay})
	return nil
}
func (h *fakeHost) Cancel(id string) error {
	delete(h.schedules, id)
	delete(h.scheduleTimes, id)
	return nil
}
func (h *fakeHost) JSON(uri string) (string, error) {
	h.jsonCalls++
	h.uris = append(h.uris, uri)
	if h.onJSON != nil {
		h.onJSON()
	}
	if h.raw != "" {
		return h.raw, h.jsonError
	}
	u, _ := url.Parse(uri)
	b, _ := json.Marshal(map[string]any{"subsonic-response": map[string]any{"status": "ok", "song": map[string]any{"id": u.Query().Get("id"), "title": "Stale API title", "coverArt": "al-1", "path": "private/path"}}})
	return string(b), h.jsonError
}
func (h *fakeHost) Raw(uri string) (string, []byte, error) {
	h.uris = append(h.uris, uri)
	return h.mime, h.image, h.rawError
}
func (h *fakeHost) Send(r Request) (*Response, error) {
	h.requests = append(h.requests, r)
	if h.send != nil {
		return h.send(r)
	}
	if strings.HasSuffix(r.URL, "/artwork") {
		return &Response{Status: 200, Body: []byte(`{}`)}, nil
	}
	var s Snapshot
	_ = json.Unmarshal(r.Body, &s)
	return ack(s, nil), nil
}
func ack(s Snapshot, missing []string) *Response {
	b, _ := json.Marshal(map[string]any{"accepted": true, "instance_id": s.Instance, "accepted_sequence": s.Sequence, "missing_artwork": missing})
	return &Response{Status: 200, Body: b}
}
func (h *fakeHost) Now() time.Time      { return h.now }
func (h *fakeHost) ID() (string, error) { h.id++; return fmt.Sprintf("instance-%d", h.id), nil }
func snap(t *testing.T, r Request) Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal(r.Body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func tick(t *testing.T, h *fakeHost) {
	t.Helper()
	if err := (Engine{h}).Tick(); err != nil {
		t.Fatal(err)
	}
}
func latest(t *testing.T, h *fakeHost) Snapshot {
	t.Helper()
	return snap(t, h.requests[len(h.requests)-1])
}

func TestSnapshotAndEventDelivery(t *testing.T) {
	h := fixture()
	e := Engine{h}
	if err := e.Init(); err != nil {
		t.Fatal(err)
	}
	var initial deadline
	_ = json.Unmarshal(h.cache["deadline"], &initial)
	if h.schedules[initial.ID] != "deadline:"+initial.ID+":0" || h.scheduleTimes[initial.ID] != h.now || len(h.requests) != 0 || h.jsonCalls != 0 {
		t.Fatal("init did delivery")
	}
	tick(t, h)
	s := latest(t, h)
	if s.Version != 2 || s.Sequence != 1 || s.Status != "selected" || s.Count != 1 || s.Selected["playerId"] != "stable-client" || s.Selected["duration"] != float64(180) || s.Selected["positionMs"] != float64(1234) {
		t.Fatal(s)
	}
	if s.Selected["title"] != "Callback title" || h.uris[0] != "getSong?u=alice&id=song" {
		t.Fatal(s, h.uris)
	}
	for i := 0; i < 1000; i++ {
		put(t, h, report(h, "stable-client", "playing", "new"))
	}
	if len(h.requests) != 1 {
		t.Fatal("callback sent HTTP")
	}
	h.now = h.now.Add(time.Second)
	tick(t, h)
	if latest(t, h).Selected["id"] != "new" || latest(t, h).Sequence != 2 {
		t.Fatal(latest(t, h))
	}
	put(t, h, report(h, "stable-client", "stopped", "new"))
	tick(t, h)
	if latest(t, h).Status != "no_match" || latest(t, h).Selected != nil || latest(t, h).Count != 0 {
		t.Fatal(latest(t, h))
	}
	if len(h.cache) != 3 || len(h.kv) != 1 || len(h.schedules) > 3 {
		t.Fatal("unbounded state")
	}
}
func TestHeartbeatPreservesSnapshotTimestamp(t *testing.T) {
	h := fixture()
	tick(t, h)
	first := latest(t, h)
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	second := latest(t, h)
	if first.At != second.At || second.Sequence != 2 || h.jsonCalls != 1 || second.Selected["positionMs"] != first.Selected["positionMs"] {
		t.Fatal(first, second, h.jsonCalls)
	}
}
func TestRetryNewestAndSequenceOne(t *testing.T) {
	h := fixture()
	h.send = func(Request) (*Response, error) { return nil, errors.New("offline") }
	tick(t, h)
	put(t, h, report(h, "stable-client", "playing", "latest"))
	h.now = h.now.Add(time.Second)
	tick(t, h)
	if len(h.requests) != 1 {
		t.Fatal("backoff bypassed")
	}
	h.now = h.now.Add(time.Second)
	tick(t, h)
	if string(h.requests[0].Body) != string(h.requests[1].Body) {
		t.Fatal("retry changed")
	}
	h.send = nil
	h.now = h.now.Add(4 * time.Second)
	tick(t, h)
	if len(h.requests) != 4 || snap(t, h.requests[2]).Sequence != 1 || latest(t, h).Sequence != 2 || latest(t, h).Selected["id"] != "latest" {
		t.Fatal("newest lost")
	}
}
func TestHARestartAndConfigRotation(t *testing.T) {
	h := fixture()
	tick(t, h)
	initial := latest(t, h)
	h.now = h.now.Add(90 * time.Second)
	h.send = func(Request) (*Response, error) {
		return &Response{Status: 200, Body: []byte(`{"accepted":false,"instance_id":null,"accepted_sequence":0}`)}, nil
	}
	tick(t, h)
	h.send = nil
	h.now = h.now.Add(time.Second)
	tick(t, h)
	fresh := latest(t, h)
	if fresh.Sequence != 1 || fresh.Instance == initial.Instance || fresh.At != initial.At {
		t.Fatal(fresh)
	}
	for _, change := range []func(){func() { h.secret = "rotated" }, func() { h.url = "https://other/api/navidrome_nowplaying/new" }, func() { h.username = "nobody" }, func() { h.username = ""; h.playerName = "other" }, func() { h.playerName = ""; h.users = []string{"bob"} }} {
		before := latest(t, h).Instance
		change()
		tick(t, h)
		if latest(t, h).Instance == before || latest(t, h).Sequence != 1 {
			t.Fatal("config did not rotate", latest(t, h))
		}
	}
	if latest(t, h).Selected != nil {
		t.Fatal("removed user still selected")
	}
}
func TestAuthorizationAndInvalidConfiguration(t *testing.T) {
	h := emptyFixture()
	e := Engine{h}
	r := report(h, "client", "playing", "song")
	r.Username = "bob"
	put(t, h, r)
	if len(h.kv) != 0 || len(h.cache) != 0 {
		t.Fatal("unauthorized report stored")
	}
	for _, u := range []string{"http://user:password@ha/api/navidrome_nowplaying/id", "http://ha/wrong", "http://ha/api/navidrome_nowplaying/id?secret=x", "ftp://ha/api/navidrome_nowplaying/id", "http://ha/api/navidrome_nowplaying/id/artwork"} {
		h.url = u
		ok, _ := e.Authorized("alice")
		if ok {
			t.Fatal(u)
		}
	}
	h.url = "https://ha/api/navidrome_nowplaying/id"
	h.usersError = errors.New("ACL failure")
	if err := e.Report(report(h, "client", "playing", "song")); err == nil {
		t.Fatal("ACL failure ignored")
	}
}
func TestImageAuthorizationMIMEAndFlags(t *testing.T) {
	h := emptyFixture()
	r := report(h, "stable-client", "playing", "song")
	r.Track.Duration = 3600
	put(t, h, r)
	h.send = func(r Request) (*Response, error) {
		if strings.HasSuffix(r.URL, "/artwork") {
			return &Response{Status: 500}, nil
		}
		return ack(snap(t, r), []string{"private", "al-1", "al-1", "unknown"}), nil
	}
	tick(t, h)
	if len(h.requests) != 2 || h.uris[1] != "getCoverArt?id=al-1&size=1000&u=alice" {
		t.Fatal(h.uris)
	}
	for _, r := range h.requests {
		if r.TimeoutMs < 5000 || r.TimeoutMs > 10000 || !r.NoFollowRedirects || r.Headers["Authorization"] != "Bearer test-secret" {
			t.Fatal(r)
		}
	}
	art := h.requests[1]
	if art.Headers["X-Navidrome-Artwork-ID"] != "al-1" || art.Headers["Content-Type"] != "image/png" || string(art.Body) != string(h.image) {
		t.Fatal(art)
	}
	h.mime = "application/json"
	h.image = []byte(`{"status":"failed"}`)
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	if len(h.requests) != 3 || latest(t, h).Sequence != 2 {
		t.Fatal("image failure blocked metadata")
	}
	h.rawError = errors.New("image failure")
	h.now = h.now.Add(90 * time.Second)
	tick(t, h)
	if len(h.requests) != 4 {
		t.Fatal("image error blocked metadata")
	}
	for _, v := range h.cache {
		if strings.Contains(string(v), string([]byte("PNG\r\n"))) {
			t.Fatal("image persisted")
		}
	}
}
func TestImageSignatures(t *testing.T) {
	for _, tc := range []struct {
		mime, body string
		ok         bool
	}{{"image/png", "\x89PNG\r\n\x1a\n", true}, {"image/jpeg", "\xff\xd8\xff", true}, {"image/gif", "GIF89a", true}, {"image/webp", "RIFFxxxxWEBP", true}, {"image/png", "not png", false}, {"text/html", "GIF89a", false}, {"image/webp", "RIFF", false}} {
		if validImage(tc.mime, []byte(tc.body)) != tc.ok {
			t.Fatal(tc)
		}
	}
	if validImage("image/png", make([]byte, maxImage+1)) {
		t.Fatal("oversize")
	}
}
func TestNormalizationAndCaps(t *testing.T) {
	n := normalize(Stream{"duration": 180.9, "positionMs": -1., "playbackRate": "bad", "playerId": "client-id", "title": strings.Repeat("x", 4097), "state": "invalid", "coverArt": "al-1", "extra": "drop"})
	if n["duration"] != int64(180) || n["playerId"] != "client-id" || len(n) != 3 {
		t.Fatal(n)
	}
	if _, ok := normalize(Stream{"playerId": float64(1)})["playerId"]; ok {
		t.Fatal("numeric unstable identity retained")
	}
	h := fixture()
	r := report(h, "new", "playing", "song")
	r.Track.Title = strings.Repeat("x", 4097)
	if err := (Engine{h}).Report(r); err == nil {
		t.Fatal("oversized report accepted")
	}
	r = report(h, "", "playing", "song")
	if err := (Engine{h}).Report(r); err == nil {
		t.Fatal("missing identity guessed")
	}
	for i := 0; i < 129; i++ {
		put(t, h, report(h, fmt.Sprint(i), "playing", "song"))
	}
	tick(t, h)
	if len(h.requests) != 0 {
		t.Fatal("oversized selection truncated")
	}
}
func TestConcurrentSchedulerLease(t *testing.T) {
	h := fixture()
	h.schedules["delivery-lease"] = "lease"
	tick(t, h)
	if len(h.requests) != 0 {
		t.Fatal("concurrent send")
	}
	delete(h.schedules, "delivery-lease")
	tick(t, h)
	put(t, h, report(h, "stable-client", "paused", "song"))
	tick(t, h)
	if len(h.requests) != 2 {
		t.Fatal("report lost")
	}
}
func TestFailedStartupRecoveryAndEmptyStartup(t *testing.T) {
	h := fixture()
	h.kvError = errors.New("playback storage unavailable")
	tick(t, h)
	h.now = h.now.Add(10 * time.Second)
	tick(t, h)
	if len(h.requests) != 0 {
		t.Fatal("invented startup snapshot")
	}
	h.kvError = nil
	h.now = h.now.Add(4 * time.Second)
	tick(t, h)
	if len(h.requests) != 1 || latest(t, h).Sequence != 1 {
		t.Fatal("startup recovery")
	}
	empty := emptyFixture()
	tick(t, empty)
	if latest(t, empty).Status != "no_match" || empty.jsonCalls != 0 {
		t.Fatal("fresh install should wait for authoritative callback")
	}
}
