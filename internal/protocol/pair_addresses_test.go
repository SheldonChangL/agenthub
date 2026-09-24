package protocol_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/protocol"
)

// A pair.request from a build that predates addresses carries address alone.
// This build decodes it, and reads no list.
func TestAnOlderPairPayloadStillDecodes(t *testing.T) {
	key := newTestKeypair(t)
	builder := pairBuilder(t, requesterID, key)
	old := struct {
		Node    protocol.NodeDescriptor `json:"node"`
		Address string                  `json:"address,omitempty"`
	}{Node: builder.Descriptor(), Address: "192.168.1.42:7463"}
	envelope, err := protocol.NewEnvelope(requesterID, protocol.TypePairRequest, protocol.At(time.Now()), old, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := protocol.ReadPairRequest(envelope); err != nil {
		t.Fatalf("an older request no longer reads: %v", err)
	}
	preferred, rest := protocol.PairAddresses(envelope)
	if preferred != "192.168.1.42:7463" || len(rest) != 0 {
		t.Fatalf("PairAddresses = %q %q", preferred, rest)
	}
	if err := compileSchema(t).Validate(roundTrip(t, envelope)); err != nil {
		t.Fatalf("the schema refuses what an older build sends: %v", err)
	}
	approve, err := protocol.NewDirectedEnvelope(receiverID, requesterID, protocol.TypePairApprove,
		protocol.At(time.Now()), struct {
			Node      protocol.NodeDescriptor `json:"node"`
			RequestID string                  `json:"requestId"`
		}{Node: builder.Descriptor(), RequestID: "pair_1"}, key)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.DecodePayload[protocol.PairApprovePayload](approve)
	if err != nil || len(payload.Addresses) != 0 {
		t.Fatalf("an older approval decodes to %+v, %v", payload, err)
	}
}

// The other direction: what an older receiver does with a new payload is
// decode it into a struct without the field. json.Unmarshal ignores what it
// does not know, which is the whole of the compatibility argument, so it is
// checked rather than assumed.
func TestAnOlderReaderIgnoresTheAddressList(t *testing.T) {
	key := newTestKeypair(t)
	envelope, err := pairBuilder(t, requesterID, key).BuildPairRequest(time.Now(),
		[]string{"192.168.1.42:7463", "10.0.0.7:7463"})
	if err != nil {
		t.Fatal(err)
	}
	var older struct {
		Node    protocol.NodeDescriptor `json:"node"`
		Address string                  `json:"address,omitempty"`
	}
	if err := json.Unmarshal(envelope.Payload, &older); err != nil {
		t.Fatalf("an older reader cannot decode the new payload: %v", err)
	}
	if older.Address != "192.168.1.42:7463" {
		t.Fatalf("an older reader sees address %q; want the preferred one", older.Address)
	}
}

// The preferred address is the first given, in address and in the list, and
// the list is cut to four however many are given.
func TestBuildPairRequestSendsThePreferredFirstAndAtMostFour(t *testing.T) {
	key := newTestKeypair(t)
	builder := pairBuilder(t, requesterID, key)
	envelope, err := builder.BuildPairRequest(time.Now(), []string{
		"10.0.0.1:7463", "", "10.0.0.1:7463", "10.0.0.2:7463", "10.0.0.3:7463", "10.0.0.4:7463", "10.0.0.5:7463",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.DecodePayload[protocol.PairRequestPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.1:7463", "10.0.0.2:7463", "10.0.0.3:7463", "10.0.0.4:7463"}
	if payload.Address != "10.0.0.1:7463" || !reflect.DeepEqual(payload.Addresses, want) {
		t.Fatalf("payload = %q %q; want %q", payload.Address, payload.Addresses, want)
	}
	if err := compileSchema(t).Validate(roundTrip(t, envelope)); err != nil {
		t.Fatalf("schema rejected the request: %v", err)
	}

	approve, err := builder.BuildPairApprove(time.Now(), receiverID, "pair_1", want)
	if err != nil {
		t.Fatal(err)
	}
	if err := compileSchema(t).Validate(roundTrip(t, approve)); err != nil {
		t.Fatalf("schema rejected the approval: %v", err)
	}
	approval, err := protocol.DecodePayload[protocol.PairApprovePayload](approve)
	if err != nil || !reflect.DeepEqual(approval.Addresses, want) {
		t.Fatalf("approval addresses = %q, %v", approval.Addresses, err)
	}

	// Nothing to offer sends no list at all, which is what an older build
	// sends too.
	none, err := builder.BuildPairRequest(time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(none.Payload), "address") {
		t.Fatalf("a request with nothing to offer carries %s", none.Payload)
	}
}

// The schema is the contract another implementation reads: it bounds the list
// at four, as the sender does.
func TestTheSchemaBoundsTheAddressList(t *testing.T) {
	key := newTestKeypair(t)
	builder := pairBuilder(t, requesterID, key)
	five := protocol.PairRequestPayload{Node: builder.Descriptor(), Address: "10.0.0.1:7463", Addresses: []string{
		"10.0.0.1:7463", "10.0.0.2:7463", "10.0.0.3:7463", "10.0.0.4:7463", "10.0.0.5:7463",
	}}
	envelope, err := protocol.NewEnvelope(requesterID, protocol.TypePairRequest, protocol.At(time.Now()), five, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := compileSchema(t).Validate(roundTrip(t, envelope)); err == nil {
		t.Fatal("the schema accepted five addresses")
	}
}
