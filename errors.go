package bedrocklight

import "strconv"

// APIError is returned for non-2xx Bedrock responses, and for server-side
// error frames embedded in the event stream.
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error implements error.
func (e *APIError) Error() string {
	return "bedrocklight: HTTP " + strconv.Itoa(e.StatusCode) + ": " + string(e.Body)
}
