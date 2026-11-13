package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

func TestOfficialPlaybackReportAdapter(t *testing.T) {
	h := emptyFixture()
	official := scrobbler.PlaybackReportRequest{
		Username: "alice", Track: scrobbler.TrackInfo{ID: "exact-track", Title: "Current callback title", Artist: "Current artist", Album: "Current album", Duration: 180.5, Path: "private-filesystem-path", LibraryID: 7, MBZRecordingID: "private-mbz-recording", AlbumArtist: "extra-album-artist", TrackNumber: 3, DiscNumber: 2},
		State: "starting", PositionMs: 40000, PlaybackRate: 1.25, PlayerId: "stable-player", PlayerName: "Same name", Timestamp: h.now.Unix() - 2,
	}
	sanitized := sanitizeReport(official)
	put(t, h, sanitized)
	tick(t, h)
	s := latest(t, h)
	if s.Selected["id"] != official.Track.ID || s.Selected["title"] != official.Track.Title || s.Selected["playerId"] != official.PlayerId || s.Selected["playerName"] != official.PlayerName || s.Selected["state"] != official.State || s.Selected["positionMs"] != float64(official.PositionMs) || s.Selected["playbackRate"] != official.PlaybackRate || s.At != "2026-01-02T03:04:03Z" {
		t.Fatal("official report fields lost", s)
	}
	b, _ := json.Marshal(sanitized)
	for _, data := range [][]byte{b, h.kv[playerKey("alice", official.PlayerId)], h.cache["session"], h.requests[0].Body} {
		for _, secret := range []string{"private-filesystem-path", "private-mbz-recording", "extra-album-artist", "LibraryID", "TrackNumber", "DiscNumber"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("nonessential official metadata escaped sanitization", secret)
			}
		}
	}
}
