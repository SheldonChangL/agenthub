package protocol

import (
	"fmt"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/address"
	"agenthub.local/agenthub/internal/model"
)

const (
	// TypeAgentMessage carries one message to a session on the recipient node.
	TypeAgentMessage = "agent.message"
	// TypeAgentAck answers one agent.message.
	TypeAgentAck = "agent.ack"
)

// maxMessageBody matches what the registry stores. Bounding it here means a
// peer cannot make this node hold an oversized body in memory before the
// storage layer would have refused it anyway.
const maxMessageBody = 32768

// maxSenderLabel bounds the sender address a peer attaches to a message.
//
// Derived from the limits of the parts a qualified address is made of, so a
// legitimate sender at every limit passes and nothing longer does. Applied here
// because this is where the value arrives from the network; the store applies
// the same bound to every write.
const maxSenderLabel = model.MaxSenderLabelLength

// MessagePayload is one message crossing between nodes.
//
// This is the first payload that carries something a person wrote rather than
// metadata this node observed. A heartbeat describes sessions; this is content.
// The distinction matters for what a node may do with it: it is queued for the
// owner to read, and nothing injects it into a provider.
type MessagePayload struct {
	// MessageID is the sender's id for this message. The recipient echoes it in
	// the ack, and uses it to recognise a redelivery of something it already
	// has rather than storing a second copy.
	MessageID string `json:"messageId"`
	// To is the session on the recipient node, in bare <provider>:<id> form.
	// The node part is already established by the envelope's recipient, so
	// repeating it here would be a second, disagreeable source of truth.
	To string `json:"to"`
	// From is the fully qualified address of the sending session, if the sender
	// named one. It is a label for the reader, not an authorisation: the
	// envelope's signature is what says which node sent this.
	From string `json:"from,omitempty"`
	Body string `json:"body"`
	// SentAt is when the sender created the message, not when it was delivered.
	SentAt time.Time `json:"sentAt"`
}

// Validate refuses a payload this node will not store.
//
// It runs before anything is written, so a peer cannot use a malformed message
// to find out what the registry does or does not contain.
func (p MessagePayload) Validate() error {
	if strings.TrimSpace(p.MessageID) == "" {
		return fmt.Errorf("message id is required")
	}
	if len(p.MessageID) > 128 {
		return fmt.Errorf("message id is too long")
	}
	if err := address.ValidateLocalSessionID(p.To); err != nil {
		return fmt.Errorf("recipient session: %w", err)
	}
	// The sender's label is stored and shown. Unbounded, a peer could attach a
	// megabyte of text to every message — storage a recipient did not agree to,
	// and a field that reaches a reader beside the body without the body's
	// framing.
	if len(p.From) > maxSenderLabel {
		return fmt.Errorf("sender address is %d bytes, over the %d limit", len(p.From), maxSenderLabel)
	}
	if strings.TrimSpace(p.Body) == "" {
		return fmt.Errorf("message body is empty")
	}
	if len(p.Body) > maxMessageBody {
		return fmt.Errorf("message body is %d bytes, over the %d byte limit", len(p.Body), maxMessageBody)
	}
	if p.From != "" {
		if _, err := address.ParseAddress(p.From, ""); err != nil {
			return fmt.Errorf("sender address: %w", err)
		}
	}
	return nil
}

// AckStatus says what the recipient did with a message.
//
// Refused and Failed are kept apart because they call for different reactions.
// A refusal is a decision the recipient's owner made — the session does not
// accept messages — and retrying will not change it. A failure is this
// recipient having a bad moment, and retrying is reasonable.
type AckStatus string

const (
	// AckQueued means the message is in the recipient's inbox. It does not mean
	// anyone has read it, and nothing injects it into a provider.
	AckQueued AckStatus = "queued"
	// AckDuplicate means the recipient already had this message id. It is a
	// success: a redelivery arriving after a lost ack must not become a second
	// copy in the inbox.
	AckDuplicate AckStatus = "duplicate"
	// AckRefused means the recipient will not take this message, and will not
	// take it later either.
	AckRefused AckStatus = "refused"
)

// AckPayload answers one agent.message.
type AckPayload struct {
	MessageID string    `json:"messageId"`
	Status    AckStatus `json:"status"`
	// Reason explains a refusal in terms the sending owner can act on. It says
	// nothing about whether the session exists: a sender that could tell
	// "no such session" from "that session declines messages" could map the
	// recipient's sessions by asking.
	Reason string `json:"reason,omitempty"`
}

// NewMessageEnvelope builds the envelope carrying one message to one node.
//
// It is directed for the same reason a heartbeat is: an undirected message
// envelope is replayable to every peer that trusts the sender, and a message is
// addressed to one session on one machine.
func NewMessageEnvelope(nodeID, recipientNodeID string, payload MessagePayload, signer Signer) (Envelope, error) {
	if err := payload.Validate(); err != nil {
		return Envelope{}, err
	}
	if err := model.ValidateNodeID(recipientNodeID); err != nil {
		return Envelope{}, fmt.Errorf("%w: recipient: %w", ErrUndirected, err)
	}
	return NewDirectedEnvelope(nodeID, recipientNodeID, TypeAgentMessage, At(payload.SentAt), payload, signer)
}
