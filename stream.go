package skipstone

import (
	"net/http"

	"github.com/kfet/skipstone/eventstream"
)

// Stream is a typed iterator over the events of a ConverseStream response.
type Stream struct {
	resp *http.Response
	dec  *eventstream.Decoder
}

// Event is a decoded streaming event.
//
// Decoded is non-nil when Type is one of the known event kinds (see
// EventMessageStart and friends). Raw always holds the unparsed JSON
// payload — useful for forward compatibility. APIError is set when
// Bedrock signals a server-side error frame inside the stream.
type Event struct {
	Type     string // raw :event-type header value
	Raw      []byte // raw JSON payload, useful for forwards-compat
	Decoded  any    // decoded into one of the Event* structs below, if known
	APIError *APIError
}

// Recv returns the next event, or io.EOF at end of stream.
func (s *Stream) Recv() (Event, error) {
	f, err := s.dec.Next()
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		Type: f.Get(":event-type"),
		Raw:  f.Payload,
	}
	// :message-type "exception" or "error" indicates a server-side error frame.
	if mt := f.Get(":message-type"); mt == "exception" || mt == "error" {
		ev.APIError = &APIError{StatusCode: 0, Body: f.Payload}
		return ev, nil
	}
	ev.Decoded = decodeEvent(ev.Type, ev.Raw)
	return ev, nil
}

// Close releases the underlying HTTP connection.
func (s *Stream) Close() error { return s.resp.Body.Close() }
