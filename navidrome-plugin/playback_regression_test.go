package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSamePlayerOldTrackInactiveCannotClearNewTrack(t *testing.T) {
	for _, active := range []string{"playing", "starting"} {
		for _, inactive := range []string{"paused", "stopped", "expired"} {
			for _, delay := range []int64{0, 1} {
				t.Run(active+"/"+inactive+"/"+time.Duration(delay).String(), func(t *testing.T) {
					h := emptyFixture()
					old := report(h, "stable-client", "playing", "A")
					put(t, h, old)
					put(t, h, report(h, "stable-client", active, "B"))
					tick(t, h)
					writes := h.kvWrites
					old.State = inactive
					old.Timestamp += delay
					put(t, h, old)
					tick(t, h)
					if h.kvWrites != writes || latest(t, h).Selected["id"] != "B" || latest(t, h).Selected["state"] != active {
						t.Fatal("previous-track callback replaced active track", latest(t, h))
					}
				})
			}
		}
	}
}

func TestSameTrackSameSecondOfficialCallbacksFollowReceivedOrder(t *testing.T) {
	// Official callbacks are serialized and have no upstream retries. Unix
	// seconds cannot prove true activity; terminal precedence would incorrectly
	// suppress legitimate same-second pause/resume and stop/play transitions.
	for _, terminal := range []string{"paused", "stopped", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			h := fixture()
			put(t, h, report(h, "stable-client", terminal, "song"))
			tick(t, h)
			if terminal == "paused" {
				if latest(t, h).Selected["state"] != "paused" {
					t.Fatal("same-second pause ignored")
				}
			} else if latest(t, h).Selected != nil {
				t.Fatal("same-second terminal ignored")
			}
			put(t, h, report(h, "stable-client", "playing", "song"))
			tick(t, h)
			if latest(t, h).Selected["state"] != "playing" {
				t.Fatal("same-second resume/play ignored")
			}
		})
	}
}

func TestEmptyTrackSDKStopStillClearsActiveTrack(t *testing.T) {
	h := fixture()
	put(t, h, report(h, "stable-client", "stopped", ""))
	tick(t, h)
	if latest(t, h).Selected != nil {
		t.Fatal("trackless SDK stop ignored")
	}
}

func TestPlaybackTextUTF8ByteBounds(t *testing.T) {
	valid := strings.Repeat("é", 2048) // 4096 UTF-8 bytes, not runes.
	invalid := []string{valid + "é", "bad\x00text", string([]byte{0xff})}
	fields := []struct {
		name string
		set  func(*PlaybackReport, string)
	}{
		{"username", func(r *PlaybackReport, s string) { r.Username = s }},
		{"playerID", func(r *PlaybackReport, s string) { r.PlayerId = s }},
		{"playerName", func(r *PlaybackReport, s string) { r.PlayerName = s }},
		{"trackID", func(r *PlaybackReport, s string) { r.Track.ID = s }},
		{"title", func(r *PlaybackReport, s string) { r.Track.Title = s }},
		{"artist", func(r *PlaybackReport, s string) { r.Track.Artist = s }},
		{"album", func(r *PlaybackReport, s string) { r.Track.Album = s }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			h := emptyFixture()
			h.users = append(h.users, valid)
			r := report(h, "client", "playing", "song")
			field.set(&r, valid)
			put(t, h, r)
			tick(t, h)
			key := field.name
			if key == "playerID" {
				key = "playerId"
			} else if key == "trackID" {
				key = "id"
			}
			if latest(t, h).Status != "selected" || latest(t, h).Selected[key] != valid {
				t.Fatal("valid boundary rejected or truncated")
			}
			for _, bad := range invalid {
				before := h.kvWrites
				h.users = append(h.users, bad)
				field.set(&r, bad)
				if err := (Engine{h}).Report(r); err == nil || h.kvWrites != before {
					t.Fatal("invalid field persisted")
				}
			}
			h.users = []string{"alice"}
			put(t, h, report(h, "client", "playing", "good"))
			tick(t, h)
			if latest(t, h).Selected["id"] != "good" {
				t.Fatal("rejected callback wedged delivery")
			}
		})
	}
}

func TestSourceFilterTextValidatedBeforePendingDelivery(t *testing.T) {
	valid := strings.Repeat("é", 2048)
	for _, field := range []string{"username", "playerName"} {
		t.Run(field, func(t *testing.T) {
			h := fixture()
			set := func(s string) {
				if field == "username" {
					h.username = s
				} else {
					h.playerName = s
				}
			}
			set(valid)
			if _, err := (Engine{h}).collect(h.users, h.now); err != nil {
				t.Fatal("valid filter boundary rejected", err)
			}
			for _, bad := range []string{valid + "é", "bad\x00filter", string([]byte{0xff})} {
				set(bad)
				if _, err := (Engine{h}).collect(h.users, h.now); err == nil {
					t.Fatal("invalid filter did not fail collection")
				}
				tick(t, h)
				var st session
				if err := json.Unmarshal(h.cache["session"], &st); err != nil {
					t.Fatal(err)
				}
				if st.Pending != nil || len(h.requests) != 0 {
					t.Fatal("invalid filter entered pending delivery")
				}
			}
			set("") // Empty means no source restriction and is valid.
			put(t, h, report(h, "stable-client", "playing", "good"))
			tick(t, h)
			if latest(t, h).Selected["id"] != "good" {
				t.Fatal("invalid filter wedged recovery")
			}
		})
	}
}

func TestArtworkUTF8ByteBoundsDoNotBlockMetadata(t *testing.T) {
	for _, art := range []string{strings.Repeat("é", 2048), strings.Repeat("é", 2049), "bad\x00art"} {
		h := fixture()
		raw, err := json.Marshal(map[string]any{"status": "ok", "song": map[string]string{"id": "song", "coverArt": art}})
		if err != nil {
			t.Fatal(err)
		}
		h.raw = string(raw)
		tick(t, h)
		s := latest(t, h)
		if s.Selected["id"] != "song" || s.Selected["title"] != "Callback title" {
			t.Fatal("art blocked metadata")
		}
		if validText(art) {
			if s.Selected["coverArt"] != art {
				t.Fatal("valid art boundary rejected")
			}
		} else if _, ok := s.Selected["coverArt"]; ok {
			t.Fatal("invalid art retained")
		}
	}
}

func TestCorruptPlaybackStorageStillBlocksCollection(t *testing.T) {
	h := fixture()
	h.kv[playerKey("alice", "stable-client")] = []byte("invalid JSON")
	if _, err := (Engine{h}).collect(h.users, h.now); err == nil {
		t.Fatal("corrupt storage accepted")
	}
	tick(t, h)
	if len(h.requests) != 0 {
		t.Fatal("corrupt storage fabricated idle")
	}
}
