package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxSnapshot = 1 << 20
const maxImage = 10 << 20
const heartbeatSeconds int64 = 90

// Host is deliberately independent of the WASM SDK so the delivery state machine
// can be exercised natively. No mutable state lives in Go globals.
type Host interface {
	Config() Config
	Users() ([]string, error)
	Get(string) ([]byte, bool, error)
	Set(string, []byte) error
	KVGet(string) ([]byte, bool, error)
	KVSetWithTTL(string, []byte, int64) error
	KVList(string) ([]string, error)
	KVGetMany([]string) (map[string][]byte, error)
	Schedule(string, string, int32) error
	Cancel(string) error
	JSON(string) (string, error)
	Raw(string) (string, []byte, error)
	Send(Request) (*Response, error)
	Now() time.Time
	ID() (string, error)
}
type Request struct {
	URL               string
	Headers           map[string]string
	Body              []byte
	TimeoutMs         int32
	NoFollowRedirects bool
}
type Response struct {
	Status int
	Body   []byte
}
type Stream map[string]any
type Config struct {
	URL, Secret, Username, PlayerName string
}
type SelectionFilters struct {
	Username   *string `json:"playback_username"`
	PlayerName *string `json:"player_name"`
}

func selectionFilters(c Config) SelectionFilters {
	var out SelectionFilters
	if c.Username != "" {
		out.Username = &c.Username
	}
	if c.PlayerName != "" {
		out.PlayerName = &c.PlayerName
	}
	return out
}

type Snapshot struct {
	Version  int              `json:"version"`
	Instance string           `json:"instance_id"`
	Sequence int64            `json:"sequence"`
	At       string           `json:"snapshot_at"`
	Status   string           `json:"selection_status"`
	Count    int              `json:"matching_stream_count"`
	Selected Stream           `json:"selected_stream"`
	Filters  SelectionFilters `json:"filters"`
	Expiry   int64            `json:"-"`
}
type session struct {
	Fingerprint     string
	Instance        string
	Sequence        int64
	Seen            string
	Snapshot        Snapshot // newest collected snapshot; never mutate Pending
	Pending         *Snapshot
	Newest          bool
	ImageCursor     int
	Next            int64
	Failures        int
	RefreshFailed   bool
	RefreshNext     int64
	RefreshFailures int
	Expiry          int64
}
type Engine struct{ H Host }

func config(urlString, secret string) bool {
	u, err := url.Parse(urlString)
	if err != nil {
		return false
	}
	_, receiver, found := strings.Cut(u.Path, "/api/navidrome_nowplaying/")
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(urlString, "#") && found && receiver != "" && !strings.Contains(receiver, "/") && secret != "" && !strings.ContainsAny(secret, "\r\n")
}
func (e Engine) Authorized(username string) (bool, error) {
	c := e.H.Config()
	if !config(c.URL, c.Secret) {
		return false, nil
	}
	users, err := e.H.Users()
	if err != nil {
		return false, err
	}
	for _, name := range users {
		if name == username {
			return true, nil
		}
	}
	return false, nil
}

// Reports are serialized by Navidrome's scrobbler worker. Only callbacks
// write player records; scheduler invocations read them without a shared index.
func (e Engine) Report(r PlaybackReport) error {
	err := e.storeReport(r)
	if err != nil && config(e.H.Config().URL, e.H.Config().Secret) {
		// A KV write may have succeeded before its refresh-token write failed.
		// Force collection on recovery rather than waiting for another report.
		_ = e.wake("report-retry", 2)
	}
	return err
}
func (e Engine) Init() error {
	// Only the explicit OnInit resets identity, and only while holding the
	// same atomic host lease as delivery. Recovery never resets session data.
	if err := e.H.Schedule("delivery-lease", "lease", 120); err != nil {
		retryErr := e.retryInit(1)
		if duplicateSchedule(err) {
			return retryErr
		}
		return err
	}
	release := true
	defer func() {
		if release {
			_ = e.H.Cancel("delivery-lease")
		}
	}()
	if err := e.H.Set("session", []byte("{}")); err != nil {
		if config(e.H.Config().URL, e.H.Config().Secret) {
			retryErr := e.retryInit(1)
			release = retryErr == nil
			return retryErr
		}
		return err
	}
	if err := e.arm(e.H.Now().Unix(), 0); err != nil {
		// Bootstrap has no earlier deadline: a failed arm needs its own wake.
		release = e.retryInit(1) == nil
		return err
	}
	return nil
}
func (e Engine) Tick() error { return e.tick("", false, 0) }
func (e Engine) tick(deadlineID string, force bool, failures int) (result error) {
	// Navidrome v0.64.2 plugins/host_scheduler.go checks/inserts IDs under
	// schedulerServiceImpl.mu; duplicate IDs fail, unlike cache get/set.
	// This lease guards concurrent one-shot invocations (fresh WASM instances).
	// It expires after a killed invocation; ordinary paths cancel it.
	if err := e.H.Schedule("delivery-lease", "lease", 120); err != nil {
		// The current owner checks the dirty token before releasing the lease.
		// A bounded retry also covers an owner killed before that check.
		var retryErr error
		if force {
			retryErr = e.wake("report-retry", 1)
		} else {
			retryErr = e.wake("busy", 1)
		}
		if !duplicateSchedule(err) {
			return err
		}
		return retryErr
	}
	release := true
	defer func() {
		if release {
			if err := e.H.Cancel("delivery-lease"); err != nil {
				if result == nil {
					result = err
				}
				return // watchdog releases the failed cancellation
			}
			// Close the final plan-read / lease-release window. A report wake
			// consumed by a contender in that window must not wait for a heartbeat.
			var st session
			b, _, err := e.H.Get("session")
			if err == nil && json.Unmarshal(b, &st) == nil {
				token, exists, err := e.H.Get("refresh")
				if err == nil && exists && string(token) != st.Seen {
					_ = e.wake("report", 1)
				}
			}
		}
	}()
	var active deadline
	b, exists, planErr := e.H.Get("deadline")
	decodeErr := json.Unmarshal(b, &active)
	if deadlineID != "" {
		if planErr != nil || !exists || decodeErr != nil || active.ID == "" {
			// Never let an unverified old deadline touch session data. Generic
			// recovery re-reads current state instead of re-hooking this ID.
			if err := e.retryDelivery(force, min(failures+1, 5)); err != nil {
				release = false // keep watchdog if even generic recovery cannot arm
			}
			if planErr != nil {
				return planErr
			}
			return errors.New("invalid active delivery deadline")
		}
		if active.ID != deadlineID {
			return nil
		}
	}
	if planErr == nil && decodeErr == nil {
		force = force || active.Force
	}
	defer func() {
		err := e.plan(result != nil, deadline{ID: deadlineID, Failures: failures, Force: force})
		if err != nil {
			// If scheduling/storage itself is unavailable, retain the watchdog.
			release = false
		}
		if result == nil {
			result = err
		}
	}()
	c := e.H.Config()
	u, secret := c.URL, c.Secret
	if !config(u, secret) {
		return e.H.Set("session", []byte("{}"))
	}
	users, err := e.H.Users()
	if err != nil {
		return err
	}
	sort.Strings(users)
	digest := sha256.Sum256([]byte(u + "\x00" + secret + "\x00" + strings.Join(users, "\x00") + "\x00" + c.Username + "\x00" + c.PlayerName))
	fingerprint := hex.EncodeToString(digest[:])
	var st session
	data, _, err := e.H.Get("session")
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &st); err != nil {
			return err
		}
	}
	now := e.H.Now().UTC()
	reset := st.Fingerprint != fingerprint || st.Instance == "" || st.Snapshot.Version != 2
	if reset {
		id, err := e.H.ID()
		if err != nil {
			return err
		}
		st = session{Fingerprint: fingerprint, Instance: id, Sequence: 1, Snapshot: Snapshot{Version: 2, Status: "no_match"}}
	}
	token, tokenExists, err := e.H.Get("refresh")
	if err != nil {
		return err
	}
	newReport := tokenExists && st.Seen != string(token)
	refresh := force || reset || newReport || (!st.RefreshFailed && st.Expiry > 0 && now.Unix() >= st.Expiry) || (st.RefreshFailed && now.Unix() >= st.RefreshNext)
	if refresh {
		// Capture the token BEFORE reading the persistent player records, so reports
		// arriving during collection remain pending for the next delivery wake.
		snapshot, err := e.collect(users, now)
		st.RefreshFailed = err != nil
		st.Seen = string(token)
		if err != nil {
			st.RefreshFailures = min(st.RefreshFailures+1, 5)
			st.RefreshNext = now.Unix() + int64(1<<st.RefreshFailures)
		} else {
			st.RefreshFailures = 0
			st.Snapshot = snapshot
			st.Expiry = snapshot.Expiry
			st.Newest = st.Pending != nil
			if st.Pending == nil {
				st.Next = 0
			}
		}
	}
	// An event may mean a session stopped or expired. Until collection succeeds,
	// do not heartbeat the old snapshot and risk reviving that session. This also
	// prevents publishing an invented empty snapshot after failed initialization.
	if st.RefreshFailed {
		if refresh {
			return e.save(st)
		}
		return nil
	}
	if len(st.Snapshot.At) == 0 {
		return e.save(st)
	}
	if now.Unix() < st.Next {
		if refresh || reset {
			return e.save(st)
		}
		return nil
	}
	return e.deliver(u, secret, now, st)
}

func (e Engine) deliver(u, secret string, now time.Time, st session) error {
	// At most the in-flight candidate and one coalesced newest snapshot.
	for sent := 0; sent < 2; sent++ {
		if st.Pending == nil {
			candidate := st.Snapshot
			candidate.Instance = st.Instance
			candidate.Sequence = st.Sequence
			st.Pending = &candidate
			// Persist BEFORE HTTP: a lost response must retry identical metadata.
			if err := e.save(st); err != nil {
				return err
			}
		}
		body, err := json.Marshal(st.Pending)
		if err != nil {
			return err
		}
		if len(body) > maxSnapshot {
			return errors.New("snapshot too large")
		}
		response, err := e.H.Send(request(u, secret, "application/json", body))
		var ack struct {
			Accepted *bool    `json:"accepted"`
			Instance *string  `json:"instance_id"`
			Sequence *int64   `json:"accepted_sequence"`
			Missing  []string `json:"missing_artwork"`
		}
		validAck := err == nil && response != nil && response.Status == 200 && len(response.Body) <= maxSnapshot && json.Unmarshal(response.Body, &ack) == nil && ack.Accepted != nil && ack.Sequence != nil
		success := validAck && *ack.Accepted && ack.Instance != nil && *ack.Instance == st.Instance && *ack.Sequence == st.Sequence
		if success {
			var streams []Stream
			if st.Pending.Status == "selected" && st.Pending.Selected != nil {
				streams = []Stream{st.Pending.Selected}
			}
			st.Pending = nil
			st.Sequence++ // sequence 1 remains pending until explicitly acknowledged
			st.Failures = 0
			st.Next = now.Unix() + heartbeatSeconds
			if st.Newest {
				st.Next = 0
			}
			// Persist the acknowledgement before slow, optional artwork work.
			if err := e.save(st); err != nil {
				return err
			}
			if st.Newest {
				st.Newest = false
				continue // newest metadata takes priority over obsolete artwork
			}
			e.images(u, secret, streams, ack.Missing, &st.ImageCursor)
			return e.save(st)
		}
		if validAck {
			// HA forgot its instance, or rejected it as retired. Never reuse that ID.
			id, err := e.H.ID()
			if err != nil {
				return err
			}
			st.Instance = id
			st.Sequence = 1
			st.Pending = nil
			st.Newest = false
			st.Failures = 0
			st.Next = now.Unix() + 1
		} else {
			st.Failures = min(st.Failures+1, 5)
			st.Next = now.Unix() + int64(1<<st.Failures) // 2..32 seconds
		}
		return e.save(st)
	}
	return e.save(st)
}
func (e Engine) save(st session) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return e.H.Set("session", b)
}
func normalize(in Stream) Stream {
	out := Stream{}
	for _, key := range []string{"id", "title", "artist", "album", "coverArt", "username", "playerName"} {
		if s, ok := in[key].(string); ok && validText(s) {
			out[key] = s
		}
	}
	if s, ok := in["state"].(string); ok && (s == "playing" || s == "paused" || s == "starting") {
		out["state"] = s
	}
	if s, ok := in["playerId"].(string); ok && validText(s) {
		out["playerId"] = s
	}
	for _, key := range []string{"duration", "positionMs", "playbackRate"} {
		if n, ok := number(in[key]); ok {
			if key != "playbackRate" {
				out[key] = int64(math.Floor(n))
			} else {
				out[key] = n
			}
		}
	}
	return out
}
func number(v any) (float64, bool) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1<<53
}
func request(u, secret, mime string, body []byte) Request {
	return Request{URL: u, Headers: map[string]string{"Authorization": "Bearer " + secret, "Content-Type": mime}, Body: body, TimeoutMs: 7000, NoFollowRedirects: true}
}
func (e Engine) images(u, secret string, streams []Stream, missing []string, cursor *int) {
	owners := map[string]string{}
	for _, s := range streams {
		id, _ := s["coverArt"].(string)
		user, _ := s["username"].(string)
		if id != "" && user != "" {
			owners[id] = user
		}
	}
	seen := map[string]bool{}
	attempts := 0
	start := *cursor
	for offset := 0; offset < len(missing); offset++ {
		index := (start + offset) % len(missing)
		id := missing[index]
		user, ok := owners[id]
		if !ok || seen[id] || strings.ContainsAny(id, "\r\n") {
			continue
		}
		seen[id] = true
		// Metadata + at most two image requests stay within the callback budget.
		if attempts == 2 {
			break
		}
		attempts++
		*cursor = (index + 1) % len(missing)
		contentType, body, err := e.H.Raw("getCoverArt?id=" + url.QueryEscape(id) + "&size=1000&u=" + url.QueryEscape(user))
		if err != nil || !validImage(contentType, body) {
			continue
		}
		contentType, _, _ = mime.ParseMediaType(contentType)
		req := request(u+"/artwork", secret, contentType, body)
		req.Headers["X-Navidrome-Artwork-ID"] = id
		// Artwork failures never affect the metadata acknowledgement. HA requests
		// uncached art again on the next heartbeat; no image cache or extra queue.
		_, _ = e.H.Send(req)
	}
}
func validImage(contentType string, b []byte) bool {
	if len(b) == 0 || len(b) > maxImage {
		return false
	}
	m, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch m {
	case "image/png":
		return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return bytes.HasPrefix(b, []byte{255, 216, 255})
	case "image/gif":
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	case "image/webp":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}
	return false
}
