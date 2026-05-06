package skipstone

import "encoding/json"

// Decoded event payloads. Only the shapes fir consumes are typed; callers
// needing more can parse Event.Raw themselves.

// EventMessageStart corresponds to :event-type messageStart.
type EventMessageStart struct {
	Role string `json:"role"`
}

// EventContentBlockStart corresponds to :event-type contentBlockStart.
type EventContentBlockStart struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse,omitempty"`
	} `json:"start"`
}

// EventContentBlockDelta corresponds to :event-type contentBlockDelta.
type EventContentBlockDelta struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Delta             struct {
		Text    string `json:"text,omitempty"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse,omitempty"`
		ReasoningContent *struct {
			Text            string `json:"text,omitempty"`
			Signature       string `json:"signature,omitempty"`
			RedactedContent []byte `json:"redactedContent,omitempty"`
		} `json:"reasoningContent,omitempty"`
		// Citation is emitted when the model attributes generated text to a
		// source document. The shape is intentionally generic — Bedrock keeps
		// extending citation locations and this surface is still in flux.
		Citation json.RawMessage `json:"citation,omitempty"`
	} `json:"delta"`
}

// EventContentBlockStop corresponds to :event-type contentBlockStop.
type EventContentBlockStop struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
}

// EventMessageStop corresponds to :event-type messageStop.
type EventMessageStop struct {
	StopReason                    string          `json:"stopReason"`
	AdditionalModelResponseFields json.RawMessage `json:"additionalModelResponseFields,omitempty"`
}

// EventMetadata corresponds to :event-type metadata.
type EventMetadata struct {
	Usage struct {
		InputTokens           int `json:"inputTokens"`
		OutputTokens          int `json:"outputTokens"`
		TotalTokens           int `json:"totalTokens"`
		CacheReadInputTokens  int `json:"cacheReadInputTokens"`
		CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	} `json:"usage"`
	Metrics struct {
		LatencyMs int `json:"latencyMs"`
	} `json:"metrics"`
	// Trace carries guardrail / model-trace info when enabled. Kept as raw
	// JSON because the Bedrock trace shape is service-specific and unstable.
	Trace json.RawMessage `json:"trace,omitempty"`
	// PerformanceConfig echoes the resolved performance tier for the request
	// (e.g. {"latency":"standard"}).
	PerformanceConfig json.RawMessage `json:"performanceConfig,omitempty"`
	// ServiceTier echoes the processing tier actually used to serve the
	// request (e.g. "standard", "priority").
	ServiceTier string `json:"serviceTier,omitempty"`
}

// decodeEvent parses raw into the typed payload for known event kinds, or
// returns nil if the kind is unknown / the payload fails to decode.
func decodeEvent(t string, raw []byte) any {
	switch t {
	case "messageStart":
		var v EventMessageStart
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockStart":
		var v EventContentBlockStart
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockDelta":
		var v EventContentBlockDelta
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "contentBlockStop":
		var v EventContentBlockStop
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "messageStop":
		var v EventMessageStop
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case "metadata":
		var v EventMetadata
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return nil
}
