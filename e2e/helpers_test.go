package e2e

import (
	"crypto/sha256"
	"encoding/hex"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
)

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func awsCreds(ak, sk, st string) awssdk.Credentials {
	return awssdk.Credentials{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		SessionToken:    st,
	}
}
