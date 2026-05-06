package skipstone

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlockMarshalText(t *testing.T) {
	b := Block{Text: "hello"}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"text":"hello"}` {
		t.Errorf("got %s", out)
	}
}

func TestBlockMarshalImage(t *testing.T) {
	b := Block{Image: &ImageBlock{Format: "png", Source: ImageSource{Bytes: []byte("PNG")}}}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"image"`) || !strings.Contains(string(out), `"format":"png"`) {
		t.Errorf("got %s", out)
	}
}

func TestBlockMarshalToolUse(t *testing.T) {
	b := Block{ToolUse: &ToolUseBlock{ToolUseID: "id", Name: "n", Input: json.RawMessage(`{}`)}}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"toolUse"`) {
		t.Errorf("got %s", out)
	}
}

func TestBlockMarshalToolResult(t *testing.T) {
	b := Block{ToolResult: &ToolResult{ToolUseID: "id", Status: "success", Content: []ToolResultContent{{Text: "ok"}}}}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"toolResult"`) {
		t.Errorf("got %s", out)
	}
}

func TestBlockMarshalReasoning(t *testing.T) {
	b := Block{Reasoning: &ReasoningBlock{Text: "think"}}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"reasoningContent"`) || !strings.Contains(string(out), `"reasoningText"`) {
		t.Errorf("got %s", out)
	}
}

func TestBlockMarshalCachePoint(t *testing.T) {
	b := Block{CachePoint: &CachePoint{Type: "default"}}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"cachePoint"`) {
		t.Errorf("got %s", out)
	}
}

func TestBlockMultipleSet(t *testing.T) {
	b := Block{ToolUse: &ToolUseBlock{}, Image: &ImageBlock{}}
	if _, err := json.Marshal(b); err == nil {
		t.Error("expected error for multiple fields")
	}
}

func TestToolChoiceMarshal(t *testing.T) {
	cases := []struct {
		tc      ToolChoice
		want    string
		wantErr bool
	}{
		{ToolChoice{Type: ToolChoiceAuto}, `{"auto":{}}`, false},
		{ToolChoice{Type: ToolChoiceAny}, `{"any":{}}`, false},
		{ToolChoice{Type: ToolChoiceTool, Name: "foo"}, `{"tool":{"name":"foo"}}`, false},
		{ToolChoice{Type: ToolChoiceTool}, "", true},
		{ToolChoice{Type: "bogus"}, "", true},
	}
	for _, c := range cases {
		out, err := json.Marshal(c.tc)
		if c.wantErr {
			if err == nil {
				t.Errorf("expected error for %+v", c.tc)
			}
			continue
		}
		if err != nil {
			t.Errorf("err: %v", err)
			continue
		}
		if string(out) != c.want {
			t.Errorf("got %s want %s", out, c.want)
		}
	}
}

func TestBuildRequestBody_Basic(t *testing.T) {
	in := &ConverseStreamInput{
		ModelID:  "anthropic.claude",
		Messages: []Message{{Role: RoleUser, Content: []Block{{Text: "hi"}}}},
		System:   []SystemBlock{{Text: "you are nice"}},
		Inference: &InferenceConfig{
			MaxTokens: ptrInt(100),
		},
	}
	body, err := buildRequestBody(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, `"messages"`) || !strings.Contains(s, `"system"`) || !strings.Contains(s, `"maxTokens":100`) {
		t.Errorf("body: %s", s)
	}
	if strings.Contains(s, `"toolConfig"`) {
		t.Errorf("toolConfig should be absent: %s", s)
	}
}

func TestBuildRequestBody_Tools(t *testing.T) {
	in := &ConverseStreamInput{
		ModelID:  "m",
		Messages: []Message{{Role: RoleUser, Content: []Block{{Text: "x"}}}},
		Tools: []Tool{{
			Name:        "lookup",
			Description: "do thing",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: &ToolChoice{Type: ToolChoiceAuto},
	}
	body, err := buildRequestBody(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, `"toolConfig"`) || !strings.Contains(s, `"toolSpec"`) || !strings.Contains(s, `"inputSchema"`) {
		t.Errorf("body: %s", s)
	}
}

func TestBuildRequestBody_Tools_NormalizesBadSchema(t *testing.T) {
	cases := map[string]json.RawMessage{
		"nil":       nil,
		"empty":     json.RawMessage(``),
		"null":      json.RawMessage(`null`),
		"bool":      json.RawMessage(`true`),
		"primitive": json.RawMessage(`42`),
		"array":     json.RawMessage(`[1,2,3]`),
		"garbage":   json.RawMessage(`not json`),
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := buildRequestBody(&ConverseStreamInput{
				ModelID:  "m",
				Messages: []Message{{Role: RoleUser, Content: []Block{{Text: "x"}}}},
				Tools:    []Tool{{Name: "t", InputSchema: schema}},
			})
			if err != nil {
				t.Fatal(err)
			}
			s := string(body)
			// Expect the inputSchema.json value to be the canonical empty object.
			if !strings.Contains(s, `"inputSchema":{"json":{"type":"object","properties":{}}}`) {
				t.Errorf("schema not normalized: %s", s)
			}
		})
	}

	// Valid object schema must pass through untouched.
	body, err := buildRequestBody(&ConverseStreamInput{
		ModelID:  "m",
		Messages: []Message{{Role: RoleUser, Content: []Block{{Text: "x"}}}},
		Tools:    []Tool{{Name: "t", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"q":{"type":"string"}`) {
		t.Errorf("valid schema mangled: %s", body)
	}
}

func TestBuildRequestBody_Errors(t *testing.T) {
	if _, err := buildRequestBody(nil); err == nil {
		t.Error("nil input")
	}
	if _, err := buildRequestBody(&ConverseStreamInput{}); err == nil {
		t.Error("missing model id")
	}
	if _, err := buildRequestBody(&ConverseStreamInput{ModelID: "m"}); err == nil {
		t.Error("missing messages")
	}
}

func ptrInt(x int) *int { return &x }
