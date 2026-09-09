package mcpserver

import (
	"strings"
	"testing"
)

func samplePush() ChannelPush {
	return ChannelPush{
		MessageID: "msg_1", Body: "look at the build",
		SenderNodeID: "node_peer0000000000000",
		SenderLabel:  "node_peer0000000000000/claude:theirs",
		Fingerprint:  "2DCF 9604 DBA9 778A 6DDD 035B", Hops: 2,
		Notice: "This message arrived from another machine and started this turn automatically.",
	}
}

// The notice comes before the body, and the body is in a section of its own.
//
// Weaker than the Codex path and knowingly so: Codex has an "untrusted" kind
// for a context fragment, so a peer's words never share a string with this
// node's. A channel push is one string, so the separation here is typographic
// and the provenance is carried out of it entirely.
func TestTheContentPutsTheNoticeBeforeTheBody(t *testing.T) {
	push := samplePush()
	content := channelContent(push)

	noticeAt := strings.Index(content, push.Notice)
	bodyAt := strings.Index(content, push.Body)
	if noticeAt < 0 || bodyAt < 0 {
		t.Fatalf("content = %q", content)
	}
	if noticeAt > bodyAt {
		t.Error("the body is read before anything says what it is")
	}
	if !strings.Contains(content, "written by someone else") {
		t.Error("nothing separates the message from the instruction around it")
	}
}

// Provenance travels as meta, which Claude Code renders as attributes of the
// <channel> element rather than as part of the message.
//
// Keys are [A-Za-z0-9_]: a key with a hyphen is dropped silently, and losing
// these would leave a stranger's words with no attribution at all — the one
// failure that turns a marked message into an anonymous one.
func TestTheProvenanceTravelsAsMetaWithUsableKeys(t *testing.T) {
	meta := channelMeta(samplePush())

	for what, want := range map[string]string{
		"the message id":  "msg_1",
		"the sender node": "node_peer0000000000000",
		"the fingerprint": "2DCF 9604 DBA9 778A 6DDD 035B",
		"the hop count":   "2",
	} {
		found := false
		for _, value := range meta {
			if value == want {
				found = true
			}
		}
		if !found {
			t.Errorf("meta does not carry %s (%q): %v", what, want, meta)
		}
	}

	for key := range meta {
		for i := 0; i < len(key); i++ {
			c := key[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '_'
			if !ok {
				t.Errorf("meta key %q has %q in it; Claude Code drops such a key without "+
					"saying so, and the message loses its attribution", key, string(c))
				break
			}
		}
	}

	// The body is not in meta: it belongs in the content, where the notice can
	// precede it.
	for key, value := range meta {
		if strings.Contains(value, "look at the build") {
			t.Errorf("meta[%q] carries the message body", key)
		}
	}
}

// Nothing optional is invented when it is absent.
func TestMetaOmitsWhatTheMessageDidNotCarry(t *testing.T) {
	meta := channelMeta(ChannelPush{MessageID: "msg_1"})
	if len(meta) != 1 || meta["agenthub_message"] != "msg_1" {
		t.Errorf("meta = %v, want only the message id", meta)
	}
}

// The capability is declared only when the owner asked for it.
//
// Declaring it registers a listener inside Claude Code. A server that declares
// one and never pushes has registered a listener for nothing — and worse, the
// owner reading their own configuration would see a channel where none is
// served. It is also the difference between a server that works everywhere and
// one that needs `--dangerously-load-development-channels` to start at all.
func TestTheChannelCapabilityIsDeclaredOnlyWhenAskedFor(t *testing.T) {
	plain, err := New(&Client{}, Binding{sessionID: "claude:x"}, "node_1234567890123456")
	if err != nil {
		t.Fatal(err)
	}
	if declared := declaredChannel(t, plain); declared {
		t.Error("a server built without WithChannel declared the channel capability")
	}

	withChannel, err := New(&Client{}, Binding{sessionID: "claude:x"}, "node_1234567890123456",
		WithChannel())
	if err != nil {
		t.Fatal(err)
	}
	if declared := declaredChannel(t, withChannel); !declared {
		t.Error("a server built with WithChannel did not declare the channel capability; " +
			"Claude Code registers no listener and every push is dropped in silence")
	}
}

// declaredChannel reports whether the built server declares the channel
// capability, read off the server the SDK was handed rather than the flag.
func declaredChannel(t *testing.T, s *server) bool {
	t.Helper()
	return s.capabilities()[ChannelCapability] != nil
}
