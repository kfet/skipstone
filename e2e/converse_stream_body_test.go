// ConverseStream cross-check: build the same request through skipstone
// and aws-sdk-go-v2's bedrockruntime client, capture the JSON request body
// each one POSTs, and assert they're structurally equivalent.
//
// We intentionally don't compare bytes — the SDK uses smithy-json which
// emits keys in declaration order, while encoding/json emits them in
// struct-field order. Both shapes are valid JSON; what matters is that the
// decoded trees are equal.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	bl "github.com/kfet/skipstone"
)

// captureRT records the body of the first request and short-circuits the
// response so neither client tries to parse one. The error we return is
// swallowed by the assertion logic in the test.
type captureRT struct {
	body []byte
}

var errStop = errors.New("stop")

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		c.body = b
	}
	return nil, errStop
}

func TestConverseStream_RequestBodyMatchesAWSSDK(t *testing.T) {
	maxTokens := int32(256)
	temperature := float32(0.5)
	stop := []string{"\n\n"}
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)

	// --- skipstone ---
	lightRT := &captureRT{}
	cl, err := bl.NewClient(
		bl.WithEndpoint("https://bedrock-runtime.us-east-1.amazonaws.com"),
		bl.WithRegion("us-east-1"),
		bl.WithStaticCredentials("AK", "SK", ""),
		bl.WithHTTPClient(&http.Client{Transport: lightRT}),
		bl.WithRetries(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	mt := int(maxTokens)
	tt := float64(temperature)
	_, _ = cl.ConverseStream(context.Background(), &bl.ConverseStreamInput{
		ModelID: "anthropic.claude-3-5-sonnet-20240620-v1:0",
		System: []bl.SystemBlock{
			{Text: "you are concise"},
		},
		Messages: []bl.Message{
			{Role: bl.RoleUser, Content: []bl.Block{{Text: "hi"}}},
		},
		Tools: []bl.Tool{{
			Name:        "lookup",
			Description: "look something up",
			InputSchema: schema,
		}},
		ToolChoice: &bl.ToolChoice{Type: bl.ToolChoiceAuto},
		Inference: &bl.InferenceConfig{
			MaxTokens:     &mt,
			Temperature:   &tt,
			StopSequences: stop,
		},
	})
	if lightRT.body == nil {
		t.Fatal("skipstone: no request captured")
	}

	// --- aws-sdk-go-v2 bedrockruntime ---
	sdkRT := &captureRT{}
	sdk := bedrockruntime.New(bedrockruntime.Options{
		Region:           "us-east-1",
		Credentials:      awssdk.NewCredentialsCache(staticCreds{}),
		HTTPClient:       &http.Client{Transport: sdkRT},
		RetryMaxAttempts: 1, // captureRT short-circuits with errStop; don't retry it
		BaseEndpoint: awssdk.String(
			"https://bedrock-runtime.us-east-1.amazonaws.com"),
	})
	var schemaDoc any
	if err := json.Unmarshal(schema, &schemaDoc); err != nil {
		t.Fatal(err)
	}
	_, _ = sdk.ConverseStream(context.Background(), &bedrockruntime.ConverseStreamInput{
		ModelId: awssdk.String("anthropic.claude-3-5-sonnet-20240620-v1:0"),
		System: []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: "you are concise"},
		},
		Messages: []brtypes.Message{{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "hi"},
			},
		}},
		ToolConfig: &brtypes.ToolConfiguration{
			Tools: []brtypes.Tool{
				&brtypes.ToolMemberToolSpec{Value: brtypes.ToolSpecification{
					Name:        awssdk.String("lookup"),
					Description: awssdk.String("look something up"),
					InputSchema: &brtypes.ToolInputSchemaMemberJson{
						Value: brdoc.NewLazyDocument(schemaDoc),
					},
				}},
			},
			ToolChoice: &brtypes.ToolChoiceMemberAuto{},
		},
		InferenceConfig: &brtypes.InferenceConfiguration{
			MaxTokens:     awssdk.Int32(maxTokens),
			Temperature:   awssdk.Float32(temperature),
			StopSequences: stop,
		},
	})
	if sdkRT.body == nil {
		t.Fatal("aws-sdk: no request captured")
	}

	lightTree := mustJSON(t, lightRT.body)
	sdkTree := mustJSON(t, sdkRT.body)
	if !reflect.DeepEqual(lightTree, sdkTree) {
		t.Errorf("body mismatch\n light: %s\n sdk:   %s",
			pretty(lightTree), pretty(sdkTree))
	}
}

// staticCreds satisfies aws.CredentialsProvider with fixed values so the SDK
// signs without consulting any credential chain.
type staticCreds struct{}

func (staticCreds) Retrieve(context.Context) (awssdk.Credentials, error) {
	return awssdk.Credentials{
		AccessKeyID:     "AK",
		SecretAccessKey: "SK",
		Source:          "static",
		CanExpire:       false,
	}, nil
}

func mustJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal: %v\n  body: %s", err, b)
	}
	return v
}

func pretty(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.String()
}
