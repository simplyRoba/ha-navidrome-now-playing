package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The host deletes a one-shot's ID AFTER its callback returns. Every delivery
// deadline therefore gets a fresh ID, even when rearmed by its own callback.
// This single cache record is written only by the delivery lease owner.
type deadline struct {
	ID       string
	At       int64
	Failures int
	Force    bool
}

func (e Engine) Callback(payload string) error {
	switch {
	case strings.HasPrefix(payload, "init:"):
		failures, _ := strconv.Atoi(strings.TrimPrefix(payload, "init:"))
		return e.tick("", false, failures)
	case payload == "lease":
		// Do not reacquire delivery-lease inside its firing callback: the host's
		// subsequent cleanup would delete the new lease. Recover after cleanup.
		if err := e.wake("recovery", 1); err != nil {
			// This firing lease is the last watchdog. If its first scheduling
			// attempt fails transiently, leave a separate bounded recovery wake.
			return e.retryDelivery(false, 1)
		}
		return nil
	case payload == "tick":
		return e.Tick()
	case payload == "refresh":
		return e.tick("", true, 0)
	case strings.HasPrefix(payload, "retry:") || strings.HasPrefix(payload, "refresh-retry:"):
		_, count, _ := strings.Cut(payload, ":")
		failures, _ := strconv.Atoi(count)
		return e.tick("", strings.HasPrefix(payload, "refresh-"), failures)
	case strings.HasPrefix(payload, "deadline:") || strings.HasPrefix(payload, "refresh-deadline:"):
		_, detail, _ := strings.Cut(payload, ":")
		parts := strings.Split(detail, ":")
		failures := 0
		if len(parts) == 2 {
			failures, _ = strconv.Atoi(parts[1])
		}
		return e.tick(parts[0], strings.HasPrefix(payload, "refresh-"), failures)
	default:
		return nil
	}
}

func (e Engine) retryInit(failures int) error {
	id := "init-" + strconv.FormatInt(e.H.Now().Unix(), 10)
	return scheduled(e.H.Schedule(id, "init:"+strconv.Itoa(failures), int32(1<<min(failures, 5))))
}

func (e Engine) retryDelivery(force bool, failures int) error {
	prefix := "retry:"
	if force {
		prefix = "refresh-retry:"
	}
	id := "storage-" + strconv.FormatInt(e.H.Now().Unix(), 10)
	return scheduled(e.H.Schedule(id, prefix+strconv.Itoa(failures), int32(1<<failures)))
}

// Reports are serialized by the host. A second bucket bounds bursts to one
// timer; its callback runs in the NEXT second, so reports arriving during that
// callback cannot reuse the ID that the host is about to delete. No report
// writes the delivery session or the deadline cache record.
func (e Engine) wake(kind string, delay int32) error {
	id := kind + "-" + strconv.FormatInt(e.H.Now().Unix(), 10)
	// Duplicate IDs mean a wake is already queued. The lease watchdog still
	// provides recovery if an invocation fails or is killed.
	payload := "tick"
	if kind == "report-retry" {
		payload = "refresh"
	}
	return scheduled(e.H.Schedule(id, payload, delay))
}

// Only duplicate IDs are successful coalescing, not host scheduling failures.
func scheduled(err error) error {
	if duplicateSchedule(err) {
		return nil
	}
	return err
}

func duplicateSchedule(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	if !strings.HasPrefix(message, "schedule ID ") || !strings.HasSuffix(message, " already exists") {
		return false
	}
	_, parseErr := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(message, "schedule ID "), " already exists"))
	return parseErr == nil
}

func (e Engine) arm(at int64, failures int, previous ...deadline) error {
	var old deadline
	b, _, _ := e.H.Get("deadline")
	_ = json.Unmarshal(b, &old)
	if len(previous) != 0 {
		if old.ID == "" {
			old.ID = previous[0].ID
		}
		old.Failures = max(old.Failures, previous[0].Failures)
		old.Force = previous[0].Force
	}
	id, err := e.H.ID()
	if err != nil {
		return err
	}
	id = "deadline-" + id
	delay := max(int64(0), at-e.H.Now().Unix())
	b, _ = json.Marshal(deadline{ID: id, At: at, Failures: failures, Force: old.Force})
	// Persist the identity BEFORE scheduling, including zero-delay bootstrap:
	// the host is allowed to start that callback before Schedule returns.
	if err = e.H.Set("deadline", b); err != nil {
		n := min(max(failures, old.Failures)+1, 5)
		// Carry backoff in the payload when cache itself is unavailable. These
		// bucket IDs are not delivery deadlines and cannot resurrect old state.
		prefix := "retry:"
		if old.Force {
			prefix = "refresh-retry:"
		}
		return scheduled(e.H.Schedule("storage-"+strconv.FormatInt(e.H.Now().Unix(), 10), prefix+strconv.Itoa(n), int32(1<<n)))
	}
	prefix := "deadline:"
	if old.Force {
		prefix = "refresh-deadline:"
	}
	if err = e.H.Schedule(id, prefix+id+":"+strconv.Itoa(failures), int32(delay)); err != nil {
		return err // the lease watchdog recovers a failed host scheduling call
	}
	if old.ID != "" {
		_ = e.H.Cancel(old.ID)
	}
	return nil
}

// Always run this before releasing the lease, including on host/storage errors.
// No idle polling: only heartbeat, retry, source expiry or an unseen report.
func (e Engine) plan(failed bool, fallback deadline) error {
	c := e.H.Config()
	if !config(c.URL, c.Secret) {
		var old deadline
		b, _, _ := e.H.Get("deadline")
		_ = json.Unmarshal(b, &old)
		if old.ID != "" {
			_ = e.H.Cancel(old.ID)
		}
		return nil
	}
	old := fallback
	b, _, err := e.H.Get("deadline")
	if err == nil {
		_ = json.Unmarshal(b, &old)
	}
	old.Failures = max(old.Failures, fallback.Failures)
	old.Force = failed && fallback.Force
	var st session
	b, _, stateErr := e.H.Get("session")
	if stateErr != nil || json.Unmarshal(b, &st) != nil {
		failed = true
	}
	now := e.H.Now().Unix()
	if failed {
		n := min(old.Failures+1, 5)
		return e.arm(now+int64(1<<n), n, old)
	}
	at := st.Next
	if st.RefreshFailed {
		at = st.RefreshNext // old metadata must not heartbeat while refresh fails
	} else if st.Expiry > 0 && (at == 0 || st.Expiry < at) {
		at = st.Expiry
	}
	token, exists, err := e.H.Get("refresh")
	if err != nil {
		return e.arm(now+2, 1, old)
	}
	if exists && string(token) != st.Seen && (at == 0 || at > now+1) {
		at = now + 1
	}
	if at <= now {
		// Immediate continuation (e.g. newest snapshot after an ACK). A one
		// second bound also avoids a zero-delay loop after damaged storage.
		at = now + 1
	}
	return e.arm(at, 0, old)
}
