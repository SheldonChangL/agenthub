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
