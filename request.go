package skipstone

import (
	"encoding/json"
	"errors"
)

// MarshalJSON emits the Bedrock-flavoured discriminated union for a content block.
// Exactly one field must be set; the empty Block (zero value) marshals as
// {"text":""} which mirrors how Bedrock treats omitted blocks.
func (b Block) MarshalJSON() ([]byte, error) {
	count := 0
	if b.Image != nil {
		count++
	}
	if b.ToolUse != nil {
		count++
	}
	if b.ToolResult != nil {
		count++
	}
	if b.Reasoning != nil {
		count++
	}
	if b.CachePoint != nil {
		count++
	}
	if count > 1 {
		return nil, errors.New("skipstone: Block has multiple non-nil fields")
	}
	switch {
	case b.Image != nil:
		return json.Marshal(map[string]any{"image": b.Image})
	case b.ToolUse != nil:
		return json.Marshal(map[string]any{"toolUse": b.ToolUse})
	case b.ToolResult != nil:
		return json.Marshal(map[string]any{"toolResult": b.ToolResult})
	case b.Reasoning != nil:
		return json.Marshal(map[string]any{"reasoningContent": map[string]any{
			"reasoningText": b.Reasoning,
		}})
	case b.CachePoint != nil:
		return json.Marshal(map[string]any{"cachePoint": b.CachePoint})
	default:
		return json.Marshal(map[string]any{"text": b.Text})
	}
}

// MarshalJSON for ToolChoice emits the AWS-shaped discriminated form:
//
//	{"auto":{}} | {"any":{}} | {"tool":{"name":"..."}}
func (tc ToolChoice) MarshalJSON() ([]byte, error) {
	switch tc.Type {
	case ToolChoiceAuto:
		return []byte(`{"auto":{}}`), nil
	case ToolChoiceAny:
		return []byte(`{"any":{}}`), nil
	case ToolChoiceTool:
		if tc.Name == "" {
			return nil, errors.New("skipstone: ToolChoice.Type=tool requires Name")
		}
		return json.Marshal(map[string]any{"tool": map[string]string{"name": tc.Name}})
	default:
		return nil, errors.New("skipstone: unknown ToolChoice.Type: " + string(tc.Type))
	}
}

// requestBody is the JSON shape POSTed to /model/{id}/converse-stream.
type requestBody struct {
	Messages                     []Message        `json:"messages"`
	System                       []SystemBlock    `json:"system,omitempty"`
	ToolConfig                   *toolConfigBody  `json:"toolConfig,omitempty"`
	InferenceConfig              *InferenceConfig `json:"inferenceConfig,omitempty"`
	AdditionalModelRequestFields json.RawMessage  `json:"additionalModelRequestFields,omitempty"`
}

type toolConfigBody struct {
	Tools      []toolEntry `json:"tools"`
	ToolChoice *ToolChoice `json:"toolChoice,omitempty"`
}

type toolEntry struct {
	ToolSpec toolSpec `json:"toolSpec"`
}

type toolSpec struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	InputSchema map[string]json.RawMessage `json:"inputSchema"`
}

// buildRequestBody assembles the JSON body and validates the input shape.
func buildRequestBody(in *ConverseStreamInput) ([]byte, error) {
	if in == nil {
		return nil, errors.New("skipstone: nil input")
	}
	if in.ModelID == "" {
		return nil, errors.New("skipstone: missing ModelID")
	}
	if len(in.Messages) == 0 {
		return nil, errors.New("skipstone: at least one Message required")
	}
	body := requestBody{
		Messages:                     in.Messages,
		System:                       in.System,
		InferenceConfig:              in.Inference,
		AdditionalModelRequestFields: in.AdditionalModelRequestFields,
	}
	if len(in.Tools) > 0 || in.ToolChoice != nil {
		tc := &toolConfigBody{ToolChoice: in.ToolChoice}
		for _, t := range in.Tools {
			tc.Tools = append(tc.Tools, toolEntry{ToolSpec: toolSpec{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: map[string]json.RawMessage{"json": normalizeToolSchema(t.InputSchema)},
			}})
		}
		body.ToolConfig = tc
	}
	return json.Marshal(body)
}

// emptyObjectSchema is the placeholder used when a tool reports an empty,
// missing, or non-object JSON Schema. The Bedrock Converse API requires
// toolConfig.tools[].toolSpec.inputSchema.json to be a JSON object and rejects
// anything else with a ValidationException.
var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// normalizeToolSchema returns raw if it is a valid JSON object, otherwise the
// canonical empty-object schema. Some MCP servers omit the schema or report a
// non-object schema (null, true, false, primitives) for parameterless tools;
// those would otherwise blow up the entire request.
func normalizeToolSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return emptyObjectSchema
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return emptyObjectSchema
	}
	if _, ok := v.(map[string]any); !ok {
		return emptyObjectSchema
	}
	return raw
}
