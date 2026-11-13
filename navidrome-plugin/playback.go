package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const playerPrefix = "playback/"
const tombstoneSeconds int64 = 24 * 60 * 60
const maxPlaybackSeconds int64 = 30 * 24 * 60 * 60

// A deliberately sanitized view of the official PlaybackReportRequest. Library
// paths, MusicBrainz fields and other filesystem metadata never enter storage.
type TrackInfo struct {
	ID, Title, Artist, Album string
	Duration                 float64
}
type PlaybackReport struct {
	Username             string
	Track                TrackInfo
	State                string
	PositionMs           int64
	PlaybackRate         float64
	PlayerId, PlayerName string
	Timestamp            int64
}
type playerRecord struct {
	Report      PlaybackReport
	ReceiptNano int64
	ExpiresAt   int64
}

func playerKey(username, id string) string {
	b, _ := json.Marshal([]string{username, id})
	sum := sha256.Sum256(b)
	return playerPrefix + hex.EncodeToString(sum[:])
}
func validText(s string) bool {
	return utf8.ValidString(s) && len(s) <= 4096 && !strings.ContainsRune(s, '\x00')
}
func (e Engine) storeReport(r PlaybackReport) error {
	allowed, err := e.Authorized(r.Username)
	if err != nil || !allowed {
		return err
	}
	// PlayerName is not unique, even within a user. Without the official stable
	// ID there is no defensible fallback; fail rather than merge two clients.
	if r.Username == "" || r.PlayerId == "" || r.Timestamp <= 0 || r.Timestamp > 253402300799 || r.PositionMs < 0 || r.PositionMs > 1<<53 {
		return errors.New("invalid playback identity or sample")
	}
	for _, s := range []string{r.Username, r.PlayerId, r.PlayerName, r.Track.ID, r.Track.Title, r.Track.Artist, r.Track.Album} {
		if !validText(s) {
			return errors.New("playback field exceeds limits")
		}
	}
	switch r.State {
	case "playing", "starting", "paused", "stopped", "expired":
	default:
		return errors.New("invalid playback state")
	}
	if _, ok := number(r.Track.Duration); !ok {
		return errors.New("invalid track duration")
	}
	if math.IsNaN(r.PlaybackRate) || math.IsInf(r.PlaybackRate, 0) || r.PlaybackRate > 1<<53 {
		return errors.New("invalid playback rate")
	}
	if r.PlaybackRate <= 0 {
		r.PlaybackRate = 1
	}
	key := playerKey(r.Username, r.PlayerId)
	data, exists, err := e.H.KVGet(key)
	if err != nil {
		return err
	}
	var old playerRecord
	if exists {
		if err = json.Unmarshal(data, &old); err != nil {
			return errors.New("invalid stored playback record")
		}
		if r.Timestamp < old.Report.Timestamp {
			return nil
		}
		// A delayed inactive callback for a known previous track cannot clear
		// the current active track, even when their Unix-second samples tie.
		// Empty IDs remain meaningful for SDK stops with no track attached.
		inactive := r.State == "paused" || r.State == "stopped" || r.State == "expired"
		active := old.Report.State == "playing" || old.Report.State == "starting"
		if inactive && active && r.Track.ID != "" && old.Report.Track.ID != "" && r.Track.ID != old.Report.Track.ID {
			return nil
		}
		// Otherwise equal-second official callbacks follow received order;
		// there are no upstream retries to deduplicate. This permits same-track
		// pause/resume and stop/play, not proof of a truly active client.
	}
	now := e.H.Now()
	receipt := now.UnixNano()
	// The refresh token is also a monotonic receipt clock. Serialized callbacks
	// make this safe; the scheduler never writes it. This orders same-second
	// callbacks even if the host clock has coarse resolution or moves backwards.
	token, _, err := e.H.Get("refresh")
	if err != nil {
		return err
	}
	previous, _ := strconv.ParseInt(string(token), 10, 64)
	if receipt <= previous {
		receipt = previous + 1
	}
	if receipt <= old.ReceiptNano {
		receipt = old.ReceiptNano + 1
	}
	seconds := int64(0)
	switch r.State {
	case "paused":
		seconds = 30 * 60
	case "playing", "starting":
		remaining := math.Max(0, r.Track.Duration-float64(r.PositionMs)/1000)/r.PlaybackRate + 5
		seconds = int64(math.Ceil(math.Min(remaining, float64(maxPlaybackSeconds))))
	}
	// Expiration is based on the server sample, not receipt time. A delayed
	// callback cannot restart the remaining-track timeout.
	record := playerRecord{Report: r, ReceiptNano: receipt, ExpiresAt: min(r.Timestamp+seconds, now.Unix()+maxPlaybackSeconds)}
	ttl := record.ExpiresAt - now.Unix() + tombstoneSeconds
	if ttl < tombstoneSeconds {
		ttl = tombstoneSeconds
	}
	if ttl > maxPlaybackSeconds+tombstoneSeconds {
		ttl = maxPlaybackSeconds + tombstoneSeconds
	}
	data, err = json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxSnapshot {
		return errors.New("playback record exceeds limits")
	}
	if err = e.H.KVSetWithTTL(key, data, ttl); err != nil {
		return err
	}
	if err := e.H.Set("refresh", []byte(strconv.FormatInt(receipt, 10))); err != nil {
		return err
	}
	return e.wake("report", 1)
}
func newer(a, b playerRecord) bool {
	if a.Report.Timestamp != b.Report.Timestamp {
		return a.Report.Timestamp > b.Report.Timestamp
	}
	if a.ReceiptNano != b.ReceiptNano {
		return a.ReceiptNano > b.ReceiptNano
	}
	return a.Report.PlayerId > b.Report.PlayerId
}
func (e Engine) collect(users []string, now time.Time) (Snapshot, error) {
	c := e.H.Config()
	if !validText(c.Username) || !validText(c.PlayerName) {
		return Snapshot{}, errors.New("source filter exceeds limits")
	}
	out := Snapshot{Version: 2, At: now.UTC().Format(time.RFC3339Nano), Status: "no_match", Filters: selectionFilters(c)}
	authorized := map[string]bool{}
	for _, u := range users {
		authorized[u] = true
	}
	keys, err := e.H.KVList(playerPrefix)
	if err != nil {
		return out, err
	}
	// Read in bounded batches; records are capped and total storage is bounded by
	// the manifest. No shared mutable index, and no callback-owned state writes.
	var active, paused []playerRecord
	for offset := 0; offset < len(keys); offset += 32 {
		batch := keys[offset:min(offset+32, len(keys))]
		values, err := e.H.KVGetMany(batch)
		if err != nil {
			return out, err
		}
		for _, key := range batch {
			data, exists := values[key]
			if !exists {
				continue
			}
			if len(data) > maxSnapshot {
				return out, errors.New("stored playback exceeds limits")
			}
			var record playerRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return out, errors.New("invalid stored playback record")
			}
			r := record.Report
			if key != playerKey(r.Username, r.PlayerId) {
				return out, errors.New("invalid stored playback identity")
			}
			if !authorized[r.Username] || (c.Username != "" && r.Username != c.Username) || (c.PlayerName != "" && r.PlayerName != c.PlayerName) {
				continue
			}
			if record.ExpiresAt <= now.Unix() {
				continue
			}
			switch r.State {
			case "playing", "starting":
				active = append(active, record)
			case "paused":
				paused = append(paused, record)
			default:
				continue
			}
			out.Count++
			if out.Count > 128 {
				return out, errors.New("playback count exceeds wire limits")
			}
			if out.Expiry == 0 || record.ExpiresAt < out.Expiry {
				out.Expiry = record.ExpiresAt
			}
		}
	}
	candidates := active
	if len(candidates) == 0 {
		candidates = paused
	}
	if len(candidates) == 0 {
		return out, nil
	}
	chosen := candidates[0]
	for _, r := range candidates[1:] {
		if r.Report.Username != chosen.Report.Username {
			out.Status = "ambiguous"
			return out, nil
		}
		if newer(r, chosen) {
			chosen = r
		}
	}
	r := chosen.Report
	out.Status = "selected"
	out.At = time.Unix(r.Timestamp, 0).UTC().Format(time.RFC3339)
	out.Selected = normalize(Stream{"id": r.Track.ID, "title": r.Track.Title, "artist": r.Track.Artist, "album": r.Track.Album, "duration": r.Track.Duration, "username": r.Username, "playerId": r.PlayerId, "playerName": r.PlayerName, "state": r.State, "positionMs": r.PositionMs, "playbackRate": r.PlaybackRate})
	if r.Track.ID != "" {
		raw, err := e.H.JSON("getSong?u=" + url.QueryEscape(r.Username) + "&id=" + url.QueryEscape(r.Track.ID))
		// Callback metadata is authoritative; artwork is optional enrichment.
		// API failures or malformed artwork must not block delivery/heartbeats.
		if err == nil {
			if art, err := songArtwork(raw, r.Track.ID); err == nil && art != "" {
				out.Selected["coverArt"] = art
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if len(b) > maxSnapshot-256 {
		return out, errors.New("snapshot exceeds wire limits")
	}
	return out, nil
}
func songArtwork(raw, id string) (string, error) {
	if len(raw) > 4*maxSnapshot {
		return "", errors.New("host metadata exceeds limits")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return "", errors.New("invalid song response")
	}
	if wrapped, ok := envelope["subsonic-response"]; ok {
		if err := json.Unmarshal(wrapped, &envelope); err != nil {
			return "", errors.New("invalid song response")
		}
	}
	var status string
	_ = json.Unmarshal(envelope["status"], &status)
	if status != "ok" {
		return "", errors.New("internal Subsonic error")
	}
	var song struct {
		ID       string `json:"id"`
		CoverArt string `json:"coverArt"`
	}
	if err := json.Unmarshal(envelope["song"], &song); err != nil {
		return "", errors.New("invalid song metadata")
	}
	if song.ID != id {
		return "", errors.New("song identity mismatch")
	}
	if !validText(song.CoverArt) {
		return "", errors.New("artwork ID exceeds limits")
	}
	return song.CoverArt, nil
}
