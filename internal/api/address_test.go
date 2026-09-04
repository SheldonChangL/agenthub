package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// Every session-addressed endpoint accepts both address forms, so a script can
// use one shape throughout.
func TestEndpointsAcceptQualifiedAddresses(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "qualified")
	// A path segment cannot hold a raw separator, so clients escape a qualified
	// address the way url.PathEscape does. Bodies carry it unescaped.
	qualified := url.PathEscape(testNodeID + "/" + id)

	cases := map[string]struct {
		method, path string
		body         any
	}{
		"read session":  {http.MethodGet, "/v1/sessions/" + qualified, nil},
		"read audience": {http.MethodGet, "/v1/sessions/" + qualified + "/audience", nil},
		"set audience":  {http.MethodPut, "/v1/sessions/" + qualified + "/audience", map[string]any{"mode": "none"}},
		"set visibility": {http.MethodPut, "/v1/sessions/" + qualified + "/visibility",
			map[string]string{"visibility": "private"}},
		"read inbox": {http.MethodGet, "/v1/inbox/" + qualified, nil},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, testCase.method, testCase.path, testCase.body)
			if response.Code != http.StatusOK {
				t.Errorf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

// A well-formed address for a machine this installation has never paired with
// is a routing answer, not a malformed request.
func TestEndpointsReportUnknownNodesAsRouting(t *testing.T) {
	store, handler := testServer(t)
	// A message leaving the machine must name a local sender whose outbound
	// gate is open; otherwise the answer is about the gate, not the route.
	id := openOutbound(t, store, handler, "remote-target")
	remoteRaw := "node_somewhere_else/" + id
	remote := url.PathEscape(remoteRaw)

	cases := map[string]struct {
		method, path string
		body         any
	}{
		"read session":  {http.MethodGet, "/v1/sessions/" + remote, nil},
		"read audience": {http.MethodGet, "/v1/sessions/" + remote + "/audience", nil},
		"set audience":  {http.MethodPut, "/v1/sessions/" + remote + "/audience", map[string]any{"mode": "none"}},
		"read inbox":    {http.MethodGet, "/v1/inbox/" + remote, nil},
		"send message":  {http.MethodPost, "/v1/messages", map[string]string{"to": remoteRaw, "from": id, "body": "hello"}},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, testCase.method, testCase.path, testCase.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("response = %d %s, want 404", response.Code, response.Body.String())
			}
			var decoded struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Error.Code != "UNKNOWN_NODE" {
				t.Errorf("code = %q, want UNKNOWN_NODE: %s", decoded.Error.Code, response.Body.String())
			}
		})
	}
}

// Malformed input stays a bad request rather than being reported as a routing
// problem, so a typo is not mistaken for an unpaired machine.
func TestMalformedAddressesAreBadRequests(t *testing.T) {
	_, handler := testServer(t)
	for name, raw := range map[string]string{
		"unknown provider": "gemini:abc",
		"no provider":      "abc",
		"empty session":    "claude:",
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, http.MethodGet, "/v1/sessions/"+raw+"/audience", nil)
			if response.Code != http.StatusBadRequest {
				t.Errorf("response = %d %s, want 400", response.Code, response.Body.String())
			}
		})
	}
}
