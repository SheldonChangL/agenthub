package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
)

func peerKey(t *testing.T) (string, string) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return identity.EncodePublicKey(public), identity.Fingerprint(public)
}

func TestPairingRequiresTheFingerprintToBelongToTheKey(t *testing.T) {
	_, handler := testServer(t)
	key, fingerprint := peerKey(t)
	otherKey, _ := peerKey(t)

	// The caller says it verified one key but sends another: that is exactly
	// the substitution the fingerprint comparison exists to catch.
	mismatch := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": "node_peer0000000000000", "displayName": "peer", "platform": "linux/amd64",
		"publicKey": otherKey, "confirmedFingerprint": fingerprint,
	})
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s, want 400", mismatch.Code, mismatch.Body.String())
	}
	var decoded struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(mismatch.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error.Code != "FINGERPRINT_MISMATCH" {
		t.Errorf("code = %q", decoded.Error.Code)
	}

	accepted := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": "node_peer0000000000000", "displayName": "peer", "platform": "linux/amd64",
		"publicKey": key, "confirmedFingerprint": fingerprint,
	})
	if accepted.Code != http.StatusCreated {
		t.Fatalf("response = %d %s, want 201", accepted.Code, accepted.Body.String())
	}
}

// Spacing and case are presentation. A person reading a fingerprint aloud must
// not fail a pairing over whitespace.
func TestPairingAcceptsAnyPresentationOfTheSameFingerprint(t *testing.T) {
	key, fingerprint := peerKey(t)

	for name, confirmed := range map[string]string{
		"as displayed": fingerprint,
		"lower case":   toLower(fingerprint),
		"no spaces":    stripSpaces(fingerprint),
	} {
		t.Run(name, func(t *testing.T) {
			_, handler := testServer(t)
			response := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
				"nodeId": "node_peer0000000000000", "displayName": "peer", "platform": "linux/amd64",
				"publicKey": key, "confirmedFingerprint": confirmed,
			})
			if response.Code != http.StatusCreated {
				t.Errorf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPairingRefusesSelfAndMalformedKeys(t *testing.T) {
	_, handler := testServer(t)
	key, fingerprint := peerKey(t)

	self := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": testNodeID, "displayName": "me", "platform": "darwin/arm64",
		"publicKey": key, "confirmedFingerprint": fingerprint,
	})
	if self.Code != http.StatusBadRequest {
		t.Errorf("pairing with self = %d %s", self.Code, self.Body.String())
	}

	malformed := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": "node_peer0000000000000", "displayName": "peer", "platform": "linux/amd64",
		"publicKey": "not-a-key", "confirmedFingerprint": fingerprint,
	})
	if malformed.Code != http.StatusBadRequest {
		t.Errorf("malformed key = %d %s", malformed.Code, malformed.Body.String())
	}
}

// Revoking a node must take away the access it held, in one step.
func TestRevokeEndpointRemovesTrustAndGrants(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "granted")
	key, fingerprint := peerKey(t)
	const peerID = "node_peer0000000000000"

	if response := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": peerID, "displayName": "peer", "platform": "linux/amd64",
		"publicKey": key, "confirmedFingerprint": fingerprint,
	}); response.Code != http.StatusCreated {
		t.Fatalf("pair = %d %s", response.Code, response.Body.String())
	}
	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience", map[string]any{
		"mode": "selected", "nodes": []string{peerID},
	}); response.Code != http.StatusOK {
		t.Fatalf("grant = %d %s", response.Code, response.Body.String())
	}

	if response := perform(t, handler, http.MethodDelete, "/v1/nodes/"+peerID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}

	audience, err := store.GetAudience(testContext(), id)
	if err != nil {
		t.Fatal(err)
	}
	if audience.PublishesTo(peerID) {
		t.Error("a revoked node kept its grant")
	}
	if audience.Mode != model.AudienceSelected {
		t.Errorf("mode changed to %q; revoking one node must not rewrite the policy", audience.Mode)
	}
}

func toLower(value string) string     { return strings.ToLower(value) }
func stripSpaces(value string) string { return strings.ReplaceAll(value, " ", "") }
func testContext() context.Context    { return context.Background() }

// A malformed node identifier is the caller's mistake, not a server failure.
// It used to reach the database and come back as a 500 with the constraint text.
func TestPairingRejectsMalformedNodeIDsAsBadRequests(t *testing.T) {
	_, handler := testServer(t)
	key, fingerprint := peerKey(t)

	for name, nodeID := range map[string]string{
		"too short":      "node_short",
		"trailing space": "node_peer0000000000000 ",
		"inner space":    "node peer 00000000000",
		"full width":     "node_ｆａｋｅ0123456789",
		"separator":      "node_a/node_b00000000",
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
				"nodeId": nodeID, "displayName": "peer", "platform": "linux/amd64",
				"publicKey": key, "confirmedFingerprint": fingerprint,
			})
			if response.Code != http.StatusBadRequest {
				t.Errorf("response = %d %s, want 400", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CHECK") {
				t.Errorf("the response leaked a database constraint: %s", response.Body.String())
			}
		})
	}
}

// Granting access to a node nobody paired with stores an authorization that
// takes effect the moment that node is ever trusted.
func TestAudienceRejectsUnpairedNodes(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "unpaired-grant")

	response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience", map[string]any{
		"mode": "selected", "nodes": []string{"node_never_paired0000"},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s, want 400", response.Code, response.Body.String())
	}
}
