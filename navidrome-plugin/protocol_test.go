package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedProtocolFixtures(t *testing.T) {
	body, err := os.ReadFile("../protocol/fixtures/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 || snapshot.Status != "selected" || snapshot.Count < 1 || snapshot.Selected == nil {
		t.Fatal("shared fixture must use protocol v2 selection", snapshot)
	}
	h := fixture()
	ack, err := os.ReadFile("../protocol/fixtures/ack.json")
	if err != nil {
		t.Fatal(err)
	}
	state := session{
		Instance: snapshot.Instance, Sequence: snapshot.Sequence, Snapshot: snapshot,
	}
	h.send = func(r Request) (*Response, error) {
		if r.URL == h.url {
			actual := snap(t, r)
			if actual.Instance != snapshot.Instance || actual.Sequence != snapshot.Sequence || actual.Version != 2 {
				t.Fatal(actual)
			}
			return &Response{Status: 200, Body: ack}, nil
		}
		return &Response{Status: 200}, nil
	}
	if err := (Engine{h}).deliver(h.url, h.secret, h.now, state); err != nil {
		t.Fatal(err)
	}
	var saved session
	if err := json.Unmarshal(h.cache["session"], &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Sequence != 2 || saved.Failures != 0 || saved.Pending != nil {
		t.Fatal("shared ack was not accepted", saved)
	}
	if len(h.requests) != 2 || h.requests[1].Headers["X-Navidrome-Artwork-ID"] != "al-1" {
		t.Fatal("shared ack did not request artwork", h.requests)
	}
}
