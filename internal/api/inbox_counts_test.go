package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

type countsBody struct {
	Counts map[string]struct {
		Held     int  `json:"held"`
		Capacity int  `json:"capacity"`
		Full     bool `json:"full"`
	} `json:"counts"`
	Capacity    int    `json:"capacity"`
	GeneratedAt string `json:"generatedAt"`
}

func readCounts(t *testing.T, handler http.Handler) countsBody {
	t.Helper()
	response := perform(t, handler, http.MethodGet, "/v1/inbox/counts", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/inbox/counts = %d %s", response.Code, response.Body.String())
	}
	var decoded countsBody
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode counts: %v (%s)", err, response.Body.String())
	}
	return decoded
}

func seedInbox(t *testing.T, store *registry.Registry, sessionID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID:                fmt.Sprintf("msg_%s_%d", sessionID, i),
			To:                sessionID,
			From:              peerNodeID,
			DestinationNodeID: testNodeID,
			Body:              "held",
		}); err != nil {
			t.Fatalf("seeding %s: %v", sessionID, err)
		}
	}
}

// TestInboxCountsAnswersEveryLocalSessionInOneRead is the endpoint the desktop
// badge is built on (issue #146).
//
// One request for the whole list, because the alternative — GET /v1/inbox/{id}
// per row — is what kept the badge out of the redesign. A session holding
// nothing is absent rather than zero, and that is documented rather than
// incidental: the reader treats a missing key as 0 and has no map at all when
// the read failed, which are different facts.
func TestInboxCountsAnswersEveryLocalSessionInOneRead(t *testing.T) {
	store, owner, _ := testSurfaces(t)

	// Before anything is stored. An answer, not a 404: "every inbox is empty"
	// is a fact worth being able to read.
	empty := readCounts(t, owner)
	if len(empty.Counts) != 0 {
		t.Fatalf("a node holding nothing answered with %d entries: %v", len(empty.Counts), empty.Counts)
	}
	if empty.GeneratedAt == "" {
		t.Error("the answer carries no generatedAt, so a reader cannot say how old a badge is")
	}
	if empty.Capacity != registry.MaxInboxMessages {
		t.Errorf("capacity = %d; want %d", empty.Capacity, registry.MaxInboxMessages)
	}

	one := acceptingLocalSession(t, store, owner, "claude:counts-one")
	many := acceptingLocalSession(t, store, owner, "codex:counts-many")
	quiet := acceptingLocalSession(t, store, owner, "claude:counts-quiet")
	seedInbox(t, store, one, 1)
	seedInbox(t, store, many, 3)

	counts := readCounts(t, owner)
	if counts.Counts[one].Held != 1 {
		t.Errorf("held for %s = %d; want 1", one, counts.Counts[one].Held)
	}
	if counts.Counts[many].Held != 3 {
		t.Errorf("held for %s = %d; want 3", many, counts.Counts[many].Held)
	}
	if _, present := counts.Counts[quiet]; present {
		t.Errorf("%s holds nothing and still appears: %v", quiet, counts.Counts[quiet])
	}
	if counts.Counts[one].Capacity != registry.MaxInboxMessages {
		t.Errorf("per-entry capacity = %d; want %d", counts.Counts[one].Capacity, registry.MaxInboxMessages)
	}
	if counts.Counts[one].Full {
		t.Error("an inbox holding one of five hundred is reported full")
	}

	// The batch and the single read are two views of one fact, and they sit on
	// the same screen: the badge on the row and the drawer opened over it.
	page := perform(t, owner, http.MethodGet, "/v1/inbox/"+many, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /v1/inbox/%s = %d %s", many, page.Code, page.Body.String())
	}
	var single struct {
		Held     int  `json:"held"`
		Capacity int  `json:"capacity"`
		Full     bool `json:"full"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.Held != counts.Counts[many].Held || single.Capacity != counts.Counts[many].Capacity {
		t.Errorf("the batch says %+v and the single read says %+v for the same inbox",
			counts.Counts[many], single)
	}
}

// TestInboxCountsMarksAFullInbox: a full inbox is deferring new messages right
// now, which is the one state a bare number does not carry.
func TestInboxCountsMarksAFullInbox(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:counts-full")
	seedInbox(t, store, session, registry.MaxInboxMessages)

	counts := readCounts(t, owner)
	entry, ok := counts.Counts[session]
	if !ok {
		t.Fatalf("a full inbox is missing from the counts: %v", counts.Counts)
	}
	if entry.Held != registry.MaxInboxMessages || !entry.Full {
		t.Errorf("a full inbox reads as %+v; want held %d and full", entry, registry.MaxInboxMessages)
	}
}

// TestInboxCountsIsNotOnThePeerSurface is the authorisation this endpoint needs,
// and it is enforced by the route not existing rather than by a check.
//
// One answer naming every local session and how much each is carrying is an
// inventory of this machine. GET /v1/inbox/{id} is owner-only for the same
// reason, and a batch of it must not be a way around that.
func TestInboxCountsIsNotOnThePeerSurface(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:counts-peer")
	seedInbox(t, store, session, 2)

	// The owner surface answers, so the peer surface's refusal below is about
	// the surface and not about the endpoint being broken.
	if got := readCounts(t, owner).Counts[session].Held; got != 2 {
		t.Fatalf("the owner surface says %d; want 2", got)
	}

	response := perform(t, peers, http.MethodGet, "/v1/inbox/counts", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("the peer surface answered GET /v1/inbox/counts with %d %s; it must have no such route",
			response.Code, response.Body.String())
	}
	if body := response.Body.String(); len(body) > 0 && jsonHasKey(t, body, "counts") {
		t.Errorf("the peer surface's refusal carries counts: %s", body)
	}
}

func jsonHasKey(t *testing.T, body, key string) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return false
	}
	_, ok := decoded[key]
	return ok
}
