module github.com/kfet/bedrock-light/e2e

go 1.24

require (
	github.com/aws/aws-sdk-go-v2 v1.41.3
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.6
	github.com/kfet/bedrock-light v0.0.0
)

require github.com/aws/smithy-go v1.24.2 // indirect

replace github.com/kfet/bedrock-light => ..
