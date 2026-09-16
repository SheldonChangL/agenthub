package nodeconfig

import (
	"errors"
	"strings"
	"testing"
)

// The three reasons exist to put different buttons on a desktop, so the thing
// worth asserting is that each state produces its own — a classifier that
// answered "unusable" to everything would compile, pass a shape check, and send
// every owner to the same dead end.
func TestClassifyListenFailure(t *testing.T) {
	interfaces := []string{"127.0.0.1/8", "192.168.1.10/24"}
	refuse := func(string, string) error { return errors.New("refused") }
	accept := func(string, string) error { return nil }

	for _, testCase := range []struct {
		name    string
		address string
		probe   func(string, string) error
		want    string
	}{
		{
			// The case this whole path was written for: the address was on a
			// cable that is no longer plugged in.
			name:    "address this machine no longer holds",
			address: "122.122.122.1:7463",
			probe:   accept,
			want:    ListenAddressGone,
		},
		{
			// The address is here and the machine will serve it, so what failed
			// was the port.
			name:    "address is here and the machine will serve it",
			address: "192.168.1.10:7463",
			probe:   accept,
			want:    ListenPortInUse,
		},
		{
			// The address is here and the machine still refuses it. Not a port
			// number problem, and offering the owner a different port would
			// waste a restart.
			name:    "address is here and the machine refuses it anyway",
			address: "192.168.1.10:7463",
			probe:   refuse,
			want:    ListenUnusable,
		},
		{
			// Loopback is here even when the interface list does not say so.
			// Answering address_gone for 127.0.0.1 would send an owner to look
			// at their cables over a busy port.
			name:    "loopback is always held",
			address: "127.0.0.1:7463",
			probe:   accept,
			want:    ListenPortInUse,
		},
		{
			name:    "not an address at all",
			address: "not-an-address",
			probe:   accept,
			want:    ListenUnusable,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ClassifyListenFailure(testCase.address, interfaces, testCase.probe); got != testCase.want {
				t.Errorf("ClassifyListenFailure(%q) = %q, want %q", testCase.address, got, testCase.want)
			}
		})
	}
}

// A bare address is accepted beside a CIDR one so that neither caller has to
// reshape what it has. net.Interfaces gives CIDR; a test gives what is legible.
func TestClassifyListenFailureAcceptsBareInterfaceAddresses(t *testing.T) {
	if got := ClassifyListenFailure("192.168.1.10:7463", []string{"192.168.1.10"},
		func(string, string) error { return nil }); got != ListenPortInUse {
		t.Errorf("bare interface address: got %q, want %q", got, ListenPortInUse)
	}
}

// Every reason has to name the address that failed and the address being served
// instead. A sentence missing either half leaves the owner to guess which of
// the two the panel is talking about.
func TestListenFailureReasonNamesBothAddresses(t *testing.T) {
	for _, reason := range []string{ListenAddressGone, ListenPortInUse, ListenUnusable, "something new"} {
		sentence := ListenFailureReason(reason, "122.122.122.1:7463", "127.0.0.1:7463")
		if !strings.Contains(sentence, "122.122.122.1:7463") {
			t.Errorf("%s: sentence does not name the address that failed: %q", reason, sentence)
		}
		if !strings.Contains(sentence, "127.0.0.1:7463") {
			t.Errorf("%s: sentence does not name what is being served: %q", reason, sentence)
		}
	}
}
