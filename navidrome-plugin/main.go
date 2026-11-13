package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

type runtimeHost struct{}

func (runtimeHost) Config() Config {
	u, _ := pdk.GetConfig("push_url")
	s, _ := pdk.GetConfig("secret")
	username, _ := pdk.GetConfig("playback_username")
	player, _ := pdk.GetConfig("player_name")
	return Config{URL: u, Secret: s, Username: strings.TrimSpace(username), PlayerName: strings.TrimSpace(player)}
}
func (runtimeHost) Users() ([]string, error) {
	users, err := host.UsersGetUsers()
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.UserName)
	}
	return names, err
}
func (runtimeHost) Get(key string) ([]byte, bool, error)   { return host.CacheGetBytes(key) }
func (runtimeHost) Set(key string, b []byte) error         { return host.CacheSetBytes(key, b, 0) }
func (runtimeHost) KVGet(key string) ([]byte, bool, error) { return host.KVStoreGet(key) }
func (runtimeHost) KVSetWithTTL(key string, b []byte, ttl int64) error {
	return host.KVStoreSetWithTTL(key, b, ttl)
}
func (runtimeHost) KVList(prefix string) ([]string, error) { return host.KVStoreList(prefix) }
func (runtimeHost) KVGetMany(keys []string) (map[string][]byte, error) {
	return host.KVStoreGetMany(keys)
}
func (runtimeHost) Schedule(id, payload string, seconds int32) error {
	_, err := host.SchedulerScheduleOneTime(seconds, payload, id)
	return err
}
func (runtimeHost) Cancel(id string) error                 { return host.SchedulerCancelSchedule(id) }
func (runtimeHost) JSON(uri string) (string, error)        { return host.SubsonicAPICall(uri) }
func (runtimeHost) Raw(uri string) (string, []byte, error) { return host.SubsonicAPICallRaw(uri) }
func (runtimeHost) Send(r Request) (*Response, error) {
	response, err := host.HTTPSend(host.HTTPRequest{Method: "POST", URL: r.URL, Headers: r.Headers, Body: r.Body, TimeoutMs: r.TimeoutMs, NoFollowRedirects: r.NoFollowRedirects})
	if err != nil || response == nil {
		// Host errors can include URLs. Never log user-configured destinations,
		// authorization headers, request bodies or response bodies here.
		pdk.Log(pdk.LogWarn, "HA push HTTP request failed; delivery will retry")
		return nil, err
	}
	if response.StatusCode != 200 {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("HA push HTTP status %d", response.StatusCode))
	}
	return &Response{Status: int(response.StatusCode), Body: response.Body}, nil
}
func (runtimeHost) Now() time.Time { return time.Now() }
func (runtimeHost) ID() (string, error) {
	var b [24]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

type plugin struct{}

func (plugin) OnInit() error { return (Engine{runtimeHost{}}).Init() }
func (plugin) IsAuthorized(r scrobbler.IsAuthorizedRequest) (bool, error) {
	return (Engine{runtimeHost{}}).Authorized(r.Username)
}
func (plugin) NowPlaying(scrobbler.NowPlayingRequest) error { return nil }
func (plugin) Scrobble(scrobbler.ScrobbleRequest) error     { return nil }
func (plugin) PlaybackReport(r scrobbler.PlaybackReportRequest) error {
	return (Engine{runtimeHost{}}).Report(sanitizeReport(r))
}
func sanitizeReport(r scrobbler.PlaybackReportRequest) PlaybackReport {
	return PlaybackReport{
		Username: r.Username,
		Track:    TrackInfo{ID: r.Track.ID, Title: r.Track.Title, Artist: r.Track.Artist, Album: r.Track.Album, Duration: float64(r.Track.Duration)},
		State:    r.State, PositionMs: r.PositionMs, PlaybackRate: r.PlaybackRate,
		PlayerId: r.PlayerId, PlayerName: r.PlayerName, Timestamp: r.Timestamp,
	}
}
func (plugin) OnCallback(r scheduler.SchedulerCallbackRequest) error {
	return (Engine{runtimeHost{}}).Callback(r.Payload)
}
func init() { p := plugin{}; lifecycle.Register(p); scrobbler.Register(p); scheduler.Register(p) }
func main() {}
