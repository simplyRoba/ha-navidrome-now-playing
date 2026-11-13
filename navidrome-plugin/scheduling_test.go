package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type scheduleRequest struct {
	ID, Payload string
	Delay       int32
}

// Model the official host: the entry remains present during invocation and its
// captured ID is unconditionally deleted AFTER return, even if cancelled/reused.
// Locks protect host operations, not Engine invocations (fresh WASM instances).
type schedulerHarness struct {
	*fakeHost
	mu            sync.Mutex
	callbacks     int
	firing        map[string]bool
	peak          int
	rawHook       func()
	getError      error
	setError      error
	setKey        string
	getKey        string
	getOnce       bool
	scheduleError error
	scheduleKey   string
	scheduleOnce  bool
	cancelError   error
	cancelKey     string
	cancelHook    func()
}

func harness() *schedulerHarness {
	return &schedulerHarness{fakeHost: emptyFixture(), firing: map[string]bool{}}
}
func (h *schedulerHarness) Config() Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.Config()
}
func (h *schedulerHarness) Users() ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.Users()
}
func (h *schedulerHarness) Now() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
func (h *schedulerHarness) ID() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.ID()
}
func (h *schedulerHarness) Get(k string) ([]byte, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.getError != nil && (h.getKey == "" || h.getKey == k) {
		err := h.getError
		if h.getOnce {
			h.getError = nil
		}
		return nil, false, err
	}
	b, ok, err := h.fakeHost.Get(k)
	return append([]byte(nil), b...), ok, err
}
func (h *schedulerHarness) Set(k string, b []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.setError != nil && (h.setKey == "" || k == h.setKey) {
		return h.setError
	}
	return h.fakeHost.Set(k, b)
}
func (h *schedulerHarness) KVGet(k string) ([]byte, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.KVGet(k)
}
func (h *schedulerHarness) KVSetWithTTL(k string, b []byte, ttl int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.KVSetWithTTL(k, b, ttl)
}
func (h *schedulerHarness) KVList(p string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.KVList(p)
}
func (h *schedulerHarness) KVGetMany(keys []string) (map[string][]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeHost.KVGetMany(keys)
}
func (h *schedulerHarness) Schedule(id, p string, delay int32) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scheduleError != nil && (h.scheduleKey == "" || strings.HasPrefix(id, h.scheduleKey)) {
		err := h.scheduleError
		if h.scheduleOnce {
			h.scheduleError = nil
		}
		return err
	}
	err := h.fakeHost.Schedule(id, p, delay)
	h.peak = max(h.peak, len(h.schedules))
	return err
}
func (h *schedulerHarness) Cancel(id string) error {
	h.mu.Lock()
	hook := h.cancelHook
	if id == "delivery-lease" {
		h.cancelHook = nil
	} else {
		hook = nil
	}
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelError != nil && (h.cancelKey == "" || h.cancelKey == id) {
		return h.cancelError
	}
	return h.fakeHost.Cancel(id)
}
func (h *schedulerHarness) JSON(uri string) (string, error) {
	h.mu.Lock()
	hook := h.onJSON
	h.onJSON = nil
	result, err := h.fakeHost.JSON(uri)
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, err
}
func (h *schedulerHarness) Raw(uri string) (string, []byte, error) {
	h.mu.Lock()
	hook := h.rawHook
	h.rawHook = nil
	mime, b, err := h.fakeHost.Raw(uri)
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return mime, b, err
}
func (h *schedulerHarness) Send(r Request) (*Response, error) {
	h.mu.Lock()
	h.requests = append(h.requests, r)
	send := h.send
	h.mu.Unlock()
	if send != nil {
		return send(r)
	}
	if strings.HasSuffix(r.URL, "/artwork") {
		return &Response{Status: 200, Body: []byte(`{}`)}, nil
	}
	var s Snapshot
	_ = json.Unmarshal(r.Body, &s)
	return ack(s, nil), nil
}
func (h *schedulerHarness) put(t *testing.T, state, track string) {
	t.Helper()
	r := report(h.fakeHost, "player", state, track)
	if err := (Engine{h}).Report(r); err != nil {
		t.Fatal(err)
	}
}
func (h *schedulerHarness) start(t *testing.T) {
	t.Helper()
	if err := (Engine{h}).Init(); err != nil {
		t.Fatal(err)
	}
	h.advance(t, 0)
}
func (h *schedulerHarness) fire(id, payload string) error {
	h.mu.Lock()
	h.callbacks++
	h.firing[id] = true
	h.mu.Unlock()
	err := (Engine{h}).Callback(payload)
	h.mu.Lock()
	delete(h.schedules, id)
	delete(h.scheduleTimes, id)
	delete(h.firing, id)
	h.mu.Unlock()
	return err
}
func (h *schedulerHarness) advance(t *testing.T, duration time.Duration) {
	t.Helper()
	h.mu.Lock()
	until := h.now.Add(duration)
	h.mu.Unlock()
	for i := 0; i < 10000; i++ {
		h.mu.Lock()
		id := ""
		var at time.Time
		for key, due := range h.scheduleTimes {
			if _, ok := h.schedules[key]; ok && !h.firing[key] && !due.After(until) && (id == "" || due.Before(at) || due.Equal(at) && key < id) {
				id, at = key, due
			}
		}
		if id == "" {
			h.now = until
			h.mu.Unlock()
			return
		}
		h.now = at
		payload := h.schedules[id]
		h.mu.Unlock()
		// Callback errors are logged by the real host, which still deletes the ID.
		_ = h.fire(id, payload)
		h.mu.Lock()
		if h.now.After(until) {
			until = h.now
		}
		h.mu.Unlock()
	}
	t.Fatal("unbounded scheduler loop")
}

func TestOneShotIdleHeartbeatBudgetAndFreshDeadline(t *testing.T) {
	h := harness()
	h.start(t)
	if len(h.scheduleCalls) < 4 || h.scheduleCalls[1].Delay != 0 {
		t.Fatal("bootstrap not immediate one-shot")
	}
	first := h.scheduleCalls[1].ID
	if _, ok := h.schedules[first]; ok {
		t.Fatal("firing deadline not cleaned up")
	}
	if len(h.schedules) != 1 {
		t.Fatal("new deadline lost to old callback cleanup", h.schedules)
	}
	cacheEntries := len(h.cache)
	h.advance(t, 89*time.Second)
	if h.callbacks != 1 || len(h.requests) != 1 {
		t.Fatal("heartbeat fired before 90s", h.callbacks, len(h.requests))
	}
	h.advance(t, time.Second)
	if h.callbacks != 2 || len(h.requests) != 2 {
		t.Fatal("heartbeat did not fire at 90s", h.callbacks, len(h.requests))
	}
	h.advance(t, time.Hour-90*time.Second)
	if h.callbacks != 41 || len(h.requests) != 41 || h.jsonCalls != 0 || h.kvWrites != 0 || len(h.kv) != 0 || len(h.schedules) != 1 || len(h.cache) != cacheEntries || cacheEntries > 3 || h.peak > 3 {
		t.Fatal("idle must run exactly 40 heartbeats per hour plus bootstrap", h.callbacks, len(h.requests), h.schedules, len(h.cache), h.peak)
	}
}

func TestOneShotBurstCoalescesAndPreemptsHeartbeat(t *testing.T) {
	h := harness()
	h.start(t)
	for i := 0; i < 1000; i++ {
		h.put(t, "playing", "new")
	}
	if len(h.requests) != 1 || h.jsonCalls != 0 || len(h.schedules) != 2 {
		t.Fatal("report did synchronous work or created unbounded wakes", h.schedules)
	}
	h.advance(t, time.Second)
	if len(h.requests) != 2 || latest(t, h.fakeHost).Selected["id"] != "new" || h.callbacks != 2 {
		t.Fatal("report waited for heartbeat")
	}
	if len(h.cache) != 3 || len(h.kv) != 1 || len(h.schedules) != 1 {
		t.Fatal("unbounded storage/timers", h.schedules)
	}
}

func TestOneShotReportsDuringSlowWork(t *testing.T) {
	for _, stage := range []string{"collect", "http", "art"} {
		t.Run(stage, func(t *testing.T) {
			h := harness()
			h.put(t, "playing", "old")
			update := func() {
				// Cross a bucket boundary while the owner is running. Fire the
				// newly arriving event while its lease is still held.
				h.mu.Lock()
				h.now = h.now.Add(time.Second)
				h.mu.Unlock()
				h.put(t, "playing", "new")
				h.advance(t, time.Second)
			}
			switch stage {
			case "collect":
				h.onJSON = update
			case "http":
				h.send = func(r Request) (*Response, error) {
					h.mu.Lock()
					h.send = nil
					h.mu.Unlock()
					update()
					return ack(snap(t, r), nil), nil
				}
			case "art":
				h.rawHook = update
				h.send = func(r Request) (*Response, error) {
					if strings.HasSuffix(r.URL, "/artwork") {
						return &Response{Status: 200}, nil
					}
					return ack(snap(t, r), []string{"al-1"}), nil
				}
			}
			h.start(t)
			if len(h.requests) == 0 {
				t.Fatal("no delivery")
			}
			h.advance(t, time.Second)
			var newest Snapshot
			for _, request := range h.requests {
				if !strings.HasSuffix(request.URL, "/artwork") {
					newest = snap(t, request)
				}
			}
			if newest.Selected["id"] != "new" {
				t.Fatal("dirty report lost during "+stage, newest)
			}
			if len(h.schedules) > 2 {
				t.Fatal("unbounded busy recovery timers", h.schedules)
			}
		})
	}
}

func TestOneShotImmutableRetryDeadlines(t *testing.T) {
	h := harness()
	h.put(t, "playing", "old")
	h.send = func(Request) (*Response, error) { return nil, errors.New("offline") }
	h.start(t)
	original := append([]byte(nil), h.requests[0].Body...)
	h.put(t, "stopped", "old")
	h.advance(t, time.Second)
	if len(h.requests) != 1 {
		t.Fatal("event bypassed immutable pending backoff")
	}
	for _, delay := range []int64{2, 4, 8, 16, 32, 32} {
		var st session
		_ = json.Unmarshal(h.cache["session"], &st)
		// The event consumed the first second of the first backoff.
		due := time.Unix(st.Next, 0).Sub(h.now)
		if delay != 2 && due != time.Duration(delay)*time.Second {
			t.Fatal("wrong retry deadline", delay, due)
		}
		h.advance(t, due-time.Second)
		before := len(h.requests)
		h.advance(t, time.Second)
		if len(h.requests) != before+1 || !bytes.Equal(original, h.requests[len(h.requests)-1].Body) {
			t.Fatal("retry missing or mutated")
		}
	}
	h.send = nil
	var st session
	_ = json.Unmarshal(h.cache["session"], &st)
	h.advance(t, time.Unix(st.Next, 0).Sub(h.now))
	if latest(t, h.fakeHost).Selected != nil || latest(t, h.fakeHost).Sequence != 2 {
		t.Fatal("ACK did not fast-dispatch newest stop")
	}
}

func TestOneShotExpiryWithoutFurtherReports(t *testing.T) {
	for _, state := range []string{"playing", "paused"} {
		t.Run(state, func(t *testing.T) {
			h := harness()
			r := report(h.fakeHost, "player", state, "short")
			r.Track.Duration = 3
			r.PositionMs = 0
			r.PlaybackRate = 1
			if err := (Engine{h}).Report(r); err != nil {
				t.Fatal(err)
			}
			h.start(t)
			ttl := 8 * time.Second
			if state == "paused" {
				ttl = 30 * time.Minute
			}
			h.advance(t, ttl-time.Second)
			if latest(t, h.fakeHost).Selected == nil {
				t.Fatal("expired early")
			}
			h.advance(t, time.Second)
			if latest(t, h.fakeHost).Selected != nil || latest(t, h.fakeHost).Count != 0 {
				t.Fatal("expiry did not wake")
			}
		})
	}
}

func TestOneShotHostErrorsRecoverWithoutReports(t *testing.T) {
	for _, which := range []string{"users", "kv", "get", "set", "deadline", "refresh"} {
		t.Run(which, func(t *testing.T) {
			h := harness()
			h.put(t, "playing", "song")
			failure := errors.New("host unavailable")
			switch which {
			case "users":
				h.usersError = failure
			case "kv":
				h.kvError = failure
			case "get":
				h.getError = failure
			case "set":
				h.setError = failure
				h.setKey = "session"
			case "deadline":
				h.setError = failure
				h.setKey = "deadline"
			case "refresh":
				h.setError = failure
				h.setKey = "refresh"
				r := report(h.fakeHost, "player", "stopped", "song")
				if (Engine{h}).Report(r) == nil {
					t.Fatal("expected refresh write error")
				}
			}
			// Bootstrap via a real callback so cleanup on error is exercised.
			if err := h.Schedule("bootstrap", "tick", 0); err != nil {
				t.Fatal(err)
			}
			h.advance(t, 0)
			h.advance(t, 10*time.Second)
			if h.callbacks > 8 {
				t.Fatal("failure caused busy-loop", h.callbacks)
			}
			h.usersError = nil
			h.kvError = nil
			h.getError = nil
			h.setError = nil
			h.advance(t, 32*time.Second)
			if len(h.requests) == 0 {
				t.Fatal("no autonomous recovery", h.schedules)
			}
			if which == "refresh" && latest(t, h.fakeHost).Selected != nil {
				t.Fatal("partially committed report lost")
			}
		})
	}
}

func TestOneShotKilledInvocationLeaseRecovery(t *testing.T) {
	h := harness()
	// Simulate a killed instance: its ordinary deadline has been cleaned up,
	// but the atomic lease remains until its one-shot watchdog fires.
	if err := h.Schedule("delivery-lease", "lease", 120); err != nil {
		t.Fatal(err)
	}
	h.advance(t, 121*time.Second)
	if len(h.requests) != 1 || len(h.schedules) != 1 {
		t.Fatal("killed invocation left receiver dead", h.schedules)
	}
	h.advance(t, 90*time.Second)
	if len(h.requests) != 2 {
		t.Fatal("recovery lease removed its successor")
	}
}

func TestOneShotKilledInvocationRecoverySchedulingFailure(t *testing.T) {
	h := harness()
	if err := h.Schedule("delivery-lease", "lease", 120); err != nil {
		t.Fatal(err)
	}
	h.scheduleError = errors.New("scheduler temporarily unavailable")
	h.scheduleKey = "recovery-"
	h.scheduleOnce = true
	h.advance(t, 120*time.Second)
	if len(h.requests) != 0 || len(h.schedules) != 1 {
		t.Fatal("last watchdog failed to leave a recovery wake", h.schedules)
	}
	h.advance(t, 2*time.Second)
	if len(h.requests) != 1 || len(h.schedules) != 1 {
		t.Fatal("transient watchdog scheduling failure left delivery dead", h.schedules)
	}
	h.advance(t, 90*time.Second)
	if len(h.requests) != 2 {
		t.Fatal("recovery did not restore normal heartbeat cadence")
	}
}

func TestOneShotObsoleteDeadlineDoesNotResetNewIdentity(t *testing.T) {
	h := harness()
	h.start(t)
	var old deadline
	_ = json.Unmarshal(h.cache["deadline"], &old)
	if err := (Engine{h}).Init(); err != nil {
		t.Fatal(err)
	}
	h.advance(t, 0)
	instance := latest(t, h.fakeHost).Instance
	before := append([]byte(nil), h.cache["session"]...)
	if err := h.fire(old.ID, "deadline:"+old.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, h.cache["session"]) || latest(t, h.fakeHost).Instance != instance {
		t.Fatal("obsolete callback wrote new identity")
	}
	if len(h.schedules) != 1 {
		t.Fatal("obsolete callback resurrected a timer")
	}
}

func TestOneShotInvalidConfigurationDoesNotSpin(t *testing.T) {
	h := harness()
	h.url = "invalid"
	h.start(t)
	h.advance(t, 10*time.Minute)
	if len(h.requests) != 0 || h.callbacks != 1 || len(h.schedules) != 0 {
		t.Fatal("invalid config spun", h.callbacks, h.schedules)
	}
}

func TestOneShotConcurrentLeaseAndDirtyFollowup(t *testing.T) {
	h := harness()
	h.put(t, "playing", "old")
	entered, done, release := make(chan struct{}), make(chan error, 1), make(chan struct{})
	h.send = func(r Request) (*Response, error) {
		close(entered)
		<-release
		return ack(snap(t, r), nil), nil
	}
	go func() { done <- (Engine{h}).Tick() }()
	<-entered
	// Truly overlapping host operations under -race, not a Go-global mutex.
	r := report(h.fakeHost, "player", "stopped", "old")
	if err := (Engine{h}).Report(r); err != nil {
		t.Fatal(err)
	}
	if err := (Engine{h}).Tick(); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	count := len(h.requests)
	h.mu.Unlock()
	if count != 1 {
		t.Fatal("contender became a second state writer")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.send = nil
	h.advance(t, time.Second)
	if latest(t, h.fakeHost).Selected != nil {
		t.Fatal("overlapping report missed dirty followup")
	}
}

func TestOneShotBootstrapStorageRetry(t *testing.T) {
	h := harness()
	h.setError = errors.New("cache unavailable")
	h.setKey = "session"
	if err := (Engine{h}).Init(); err != nil {
		t.Fatal(err)
	}
	h.advance(t, 10*time.Second)
	if len(h.requests) != 0 || h.callbacks != 2 || len(h.schedules) != 1 {
		t.Fatal("bootstrap storage error did not back off", h.callbacks, h.schedules)
	}
	h.setError = nil
	h.advance(t, 4*time.Second)
	if len(h.requests) != 1 || latest(t, h.fakeHost).Sequence != 1 {
		t.Fatal("bootstrap did not recover autonomously")
	}
}

func TestOneShotSelectedHeartbeatDoesNotRecollect(t *testing.T) {
	h := harness()
	r := report(h.fakeHost, "player", "playing", "long")
	r.Track.Duration = 10800
	if err := (Engine{h}).Report(r); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	first := latest(t, h.fakeHost)
	h.advance(t, time.Hour)
	if h.jsonCalls != 1 || len(h.requests) != 41 || h.callbacks != 42 {
		t.Fatal("heartbeat polled metadata or ran a recurring clock", h.jsonCalls, len(h.requests), h.callbacks)
	}
	last := latest(t, h.fakeHost)
	if first.At != last.At || first.Selected["positionMs"] != last.Selected["positionMs"] {
		t.Fatal("heartbeat rewrote source sample")
	}
}

func TestOneShotPartiallyCommittedReportSurvivesRetryError(t *testing.T) {
	h := harness()
	h.put(t, "playing", "song")
	h.start(t)
	h.setError = errors.New("refresh cache unavailable")
	h.setKey = "refresh"
	r := report(h.fakeHost, "player", "stopped", "song")
	if (Engine{h}).Report(r) == nil {
		t.Fatal("expected token write failure")
	}
	h.setError = nil
	h.usersError = errors.New("users unavailable")
	h.advance(t, 3*time.Second)
	h.usersError = nil
	h.advance(t, 3*time.Second)
	if latest(t, h.fakeHost).Selected != nil {
		t.Fatal("forced collection intent lost on retry error")
	}
}

func TestOneShotInitialArmFailureRecoversEmptyScheduler(t *testing.T) {
	h := harness()
	h.scheduleError = errors.New("scheduler unavailable")
	h.scheduleKey = "deadline-"
	h.scheduleOnce = true
	if err := (Engine{h}).Init(); err == nil {
		t.Fatal("real scheduling error suppressed")
	}
	if len(h.requests) != 0 || len(h.schedules) != 1 {
		t.Fatal("bootstrap must queue its own bounded retry", h.schedules)
	}
	h.advance(t, 2*time.Second)
	if len(h.requests) != 1 || len(h.schedules) != 1 {
		t.Fatal("empty bootstrap did not recover", h.schedules)
	}
	h.advance(t, 10*time.Minute)
	if len(h.requests) != 7 {
		t.Fatal("recovered idle session must retain 90s cadence", len(h.requests))
	}
}

func TestOneShotInitRetryPreservesReportPending(t *testing.T) {
	h := harness()
	h.setError, h.setKey = errors.New("cache unavailable"), "session"
	if err := (Engine{h}).Init(); err != nil {
		t.Fatal(err)
	}
	h.setError = nil
	h.send = func(Request) (*Response, error) { return nil, errors.New("offline") }
	h.put(t, "playing", "old")
	h.advance(t, time.Second)
	before := append([]byte(nil), h.cache["session"]...)
	body := append([]byte(nil), h.requests[0].Body...)
	h.advance(t, time.Second) // init retry arrives before immutable HTTP retry
	if !bytes.Equal(before, h.cache["session"]) || len(h.requests) != 1 {
		t.Fatal("init continuation reset established delivery")
	}
	h.advance(t, time.Second)
	if len(h.requests) != 2 || !bytes.Equal(body, h.requests[1].Body) {
		t.Fatal("init retry dropped identity, sequence or immutable pending")
	}
}

func TestOneShotInitRetryAndFreshInitCannotClobberHTTPOwner(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "fresh"}[fresh], func(t *testing.T) {
			h := harness()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			h.send = func(Request) (*Response, error) {
				close(entered)
				<-release
				return nil, errors.New("offline")
			}
			go func() { done <- (Engine{h}).Tick() }()
			<-entered
			h.mu.Lock()
			before := append([]byte(nil), h.cache["session"]...)
			h.mu.Unlock()
			var err error
			if fresh {
				err = (Engine{h}).Init()
			} else {
				err = (Engine{h}).Callback("init:1")
			}
			if err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			unchanged := bytes.Equal(before, h.cache["session"])
			h.mu.Unlock()
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !unchanged {
				t.Fatal("initialization clobbered HTTP owner's pending candidate")
			}
		})
	}
}

func TestOneShotUnverifiedOldDeadlineFailsClosed(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "read failure", true: "malformed"}[corrupt], func(t *testing.T) {
			h := harness()
			h.start(t)
			var old deadline
			_ = json.Unmarshal(h.cache["deadline"], &old)
			if err := (Engine{h}).Init(); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), h.cache["session"]...)
			if corrupt {
				h.cache["deadline"] = []byte(`{"ID":`)
			} else {
				h.getError, h.getKey, h.getOnce = errors.New("cache unavailable"), "deadline", true
			}
			if err := h.fire(old.ID, "deadline:"+old.ID); err == nil {
				t.Fatal("unverified identity must fail closed")
			}
			if !bytes.Equal(before, h.cache["session"]) || len(h.requests) != 1 {
				t.Fatal("stale callback resurrected cleared session")
			}
			h.advance(t, 2*time.Second)
			if len(h.requests) != 2 || latest(t, h.fakeHost).Instance == old.ID || len(h.schedules) != 1 {
				t.Fatal("latest legitimate timer failed to recover", h.schedules)
			}
		})
	}
}

func TestOneShotReportAfterFinalPlanReadBeforeLeaseCancel(t *testing.T) {
	h := harness()
	h.start(t)
	h.cancelHook = func() {
		h.put(t, "playing", "late")
		// Consume the report wake while the owner's lease is still present.
		h.advance(t, time.Second)
	}
	if err := (Engine{h}).Tick(); err != nil {
		t.Fatal(err)
	}
	before := len(h.requests)
	h.advance(t, time.Second)
	if len(h.requests) != before+1 || latest(t, h.fakeHost).Selected["id"] != "late" {
		t.Fatal("final lease window lost report or waited for heartbeat")
	}
}

func TestOneShotCancelFailureRecoversThroughWatchdog(t *testing.T) {
	h := harness()
	h.start(t)
	h.cancelError, h.cancelKey = errors.New("cancel unavailable"), "delivery-lease"
	if err := (Engine{h}).Tick(); err == nil {
		t.Fatal("lease cancel error suppressed")
	}
	h.cancelError = nil
	h.advance(t, 121*time.Second)
	if len(h.requests) < 2 {
		t.Fatal("failed cancel left delivery permanently dead")
	}
	h.advance(t, 30*time.Second)
	if len(h.schedules) != 1 {
		t.Fatal("cancel recovery failed to settle", h.schedules)
	}
}

func TestOneShotDeadlineCancelFailureCannotResurrectOldTimer(t *testing.T) {
	h := harness()
	h.start(t)
	var old deadline
	_ = json.Unmarshal(h.cache["deadline"], &old)
	h.cancelError, h.cancelKey = errors.New("cancel unavailable"), old.ID
	h.put(t, "playing", "new")
	h.advance(t, time.Second)
	before := append([]byte(nil), h.cache["session"]...)
	if err := h.fire(old.ID, "deadline:"+old.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, h.cache["session"]) {
		t.Fatal("uncancelled stale timer mutated session")
	}
	h.cancelError = nil
	h.advance(t, 90*time.Second)
	if len(h.schedules) != 1 || latest(t, h.fakeHost).Selected["id"] != "new" {
		t.Fatal("failed deadline cancellation disrupted current timer", h.schedules)
	}
}

func TestOneShotOnlySDKDuplicatesCoalesce(t *testing.T) {
	if scheduled(errors.New(`schedule ID "report-123" already exists`)) != nil {
		t.Fatal("official duplicate not coalesced")
	}
	for _, message := range []string{"cache already exists but scheduler unavailable", "duplicate schedule backend unavailable", "scheduler unavailable"} {
		err := errors.New(message)
		if scheduled(err) != err {
			t.Fatal("real host failure coalesced", message)
		}
	}
	h := harness()
	h.scheduleError = errors.New("scheduler unavailable")
	h.scheduleKey = "delivery-lease"
	h.scheduleOnce = true
	if err := (Engine{h}).Tick(); err == nil {
		t.Fatal("lease Schedule failure treated as contention")
	}
	h.advance(t, time.Second)
	if len(h.requests) != 1 {
		t.Fatal("lease Schedule failure failed to recover")
	}
}

func TestPackageVerifierRejectsRecurringHostImport(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for package verifier test")
	}
	// Exercise the verifier's actual WASM section parser, not a text grep.
	const script = `
import json, tempfile, zipfile
from pathlib import Path
from verify_package import verify

def uint(n):
    out = bytearray()
    while n >= 128:
        out.append((n & 127) | 128)
        n >>= 7
    return bytes(out) + bytes([n])
def name(s):
    b = s.encode()
    return uint(len(b)) + b
def section(kind, body):
    return bytes([kind]) + uint(len(body)) + body
exports = ['_initialize', 'nd_on_init', 'nd_scheduler_callback',
           'nd_scrobbler_is_authorized', 'nd_scrobbler_now_playing',
           'nd_scrobbler_scrobble', 'nd_scrobbler_playback_report']
imports = ['http_send', 'users_getusers', 'subsonicapi_call', 'subsonicapi_callraw',
           'cache_getbytes', 'cache_setbytes', 'scheduler_scheduleonetime',
           'scheduler_cancelschedule', 'kvstore_get', 'kvstore_setwithttl',
           'kvstore_list', 'kvstore_getmany']
def package(path, recurring):
    names = imports + (['scheduler_schedulerecurring'] if recurring else [])
    wasm = b'\0asm\1\0\0\0'
    wasm += section(1, b'\1\x60\0\0')
    wasm += section(2, uint(len(names)) + b''.join(
        name('extism:host/user') + name(n) + b'\0\0' for n in names))
    wasm += section(7, uint(len(exports)) + b''.join(name(n) + b'\0\0' for n in exports))
    with zipfile.ZipFile(path, 'w') as z:
        z.writestr('manifest.json', json.dumps({'version': 'test'}))
        z.writestr('plugin.wasm', wasm)
with tempfile.TemporaryDirectory() as d:
    path = Path(d) / 'test.ndp'
    package(path, False)
    verify(path)
    package(path, True)
    try:
        verify(path)
    except AssertionError as e:
        assert str(e) == 'recurring scheduler imported', str(e)
    else:
        raise AssertionError('recurring import accepted')
`
	if output, err := exec.Command(python, "-B", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("package verifier guard: %v\n%s", err, output)
	}
}

func TestRuntimeSchedulerHasNoRecurringAPI(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SchedulerScheduleRecurring") || strings.Contains(string(b), "@every") {
		t.Fatal("runtime must only import/use one-shot scheduling")
	}
}
