module github.com/kfet/skipstone/e2e

go 1.24

require (
	github.com/aws/aws-sdk-go-v2 v1.41.7
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.10
	github.com/aws/aws-sdk-go-v2/service/bedrockruntime v1.50.6
	github.com/kfet/skipstone v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.23 // indirect
	github.com/aws/smithy-go v1.25.1
)

replace github.com/kfet/skipstone => ..
