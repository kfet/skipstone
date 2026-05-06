// Event-stream cross-check: decode a frame produced by the AWS SDK encoder
// using skipstone's decoder, and round-trip the other way.
package e2e

import (
	"bytes"
	"testing"

	awses "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/kfet/skipstone/eventstream"
)

func TestEventStream_DecodeAWSEncodedFrame(t *testing.T) {
	// Encode with the AWS SDK.
	msg := awses.Message{
		Headers: awses.Headers{
			{Name: ":event-type", Value: awses.StringValue("contentBlockDelta")},
			{Name: ":message-type", Value: awses.StringValue("event")},
		},
		Payload: []byte(`{"delta":{"text":"hi"}}`),
	}
	var buf bytes.Buffer
	enc := awses.NewEncoder()
	if err := enc.Encode(&buf, msg); err != nil {
		t.Fatal(err)
	}

	// Decode with skipstone.
	d := eventstream.NewDecoder(&buf)
	got, err := d.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got.Get(":event-type") != "contentBlockDelta" {
		t.Errorf("event-type: %v", got.Headers)
	}
	if string(got.Payload) != `{"delta":{"text":"hi"}}` {
		t.Errorf("payload: %s", got.Payload)
	}
}

func TestEventStream_AWSDecodesOurFrame(t *testing.T) {
	frame := eventstream.Encode(map[string]string{
		":event-type":   "messageStop",
		":message-type": "event",
	}, []byte(`{"stopReason":"end_turn"}`))

	dec := awses.NewDecoder()
	msg, err := dec.Decode(bytes.NewReader(frame), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Payload) != `{"stopReason":"end_turn"}` {
		t.Errorf("payload: %s", msg.Payload)
	}
	var et string
	for _, h := range msg.Headers {
		if h.Name == ":event-type" {
			et = h.Value.String()
		}
	}
	if et != "messageStop" {
		t.Errorf("event-type: %q", et)
	}
}
