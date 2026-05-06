package bedrocklight

import "encoding/json"

// Role identifies the speaker of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in the conversation.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
}

// Block is a content block. Exactly one of the optional pointer fields is
// non-nil. JSON marshalling emits the matching Bedrock discriminator.
type Block struct {
	Text       string          `json:"-"`
	Image      *ImageBlock     `json:"-"`
	ToolUse    *ToolUseBlock   `json:"-"`
	ToolResult *ToolResult     `json:"-"`
	Reasoning  *ReasoningBlock `json:"-"`
	CachePoint *CachePoint     `json:"-"`
}

// ImageBlock carries a base64-encoded image payload.
type ImageBlock struct {
	// Format is one of "png", "jpeg", "gif", "webp".
	Format string `json:"format"`
	// Source.Bytes is the raw image bytes — base64-encoded on the wire.
	Source ImageSource `json:"source"`
}

// ImageSource is the wire form for image data.
type ImageSource struct {
	Bytes []byte `json:"bytes"`
}

// ToolUseBlock represents an assistant-issued tool call.
type ToolUseBlock struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// ToolResult is a user-side response to a prior ToolUseBlock.
type ToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Status    string              `json:"status,omitempty"` // "success" or "error"
	Content   []ToolResultContent `json:"content"`
}

// ToolResultContent is one part of a ToolResult — text or image.
type ToolResultContent struct {
	Text  string      `json:"text,omitempty"`
	Image *ImageBlock `json:"image,omitempty"`
}

// ReasoningBlock represents extended-thinking content.
type ReasoningBlock struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// CachePoint marks a prompt-cache boundary.
type CachePoint struct {
	Type string `json:"type"` // "default"
	TTL  string `json:"ttl,omitempty"`
}

// SystemBlock is one element of the system prompt.
type SystemBlock struct {
	Text       string      `json:"text,omitempty"`
	CachePoint *CachePoint `json:"cachePoint,omitempty"`
}

// Tool describes an available tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolChoiceType selects how the model should pick tools.
type ToolChoiceType string

const (
	ToolChoiceAuto ToolChoiceType = "auto"
	ToolChoiceAny  ToolChoiceType = "any"
	ToolChoiceTool ToolChoiceType = "tool"
)

// ToolChoice constrains tool selection. Name is used only when Type=="tool".
type ToolChoice struct {
	Type ToolChoiceType
	Name string
}

// InferenceConfig matches Bedrock's inferenceConfig.
type InferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

// ConverseStreamInput is the request body for the ConverseStream API.
type ConverseStreamInput struct {
	ModelID                      string
	Messages                     []Message
	System                       []SystemBlock
	Tools                        []Tool
	ToolChoice                   *ToolChoice
	Inference                    *InferenceConfig
	AdditionalModelRequestFields json.RawMessage
}
