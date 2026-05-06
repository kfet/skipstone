// Package skipstone is a minimal, zero-dependency client for Amazon
// Bedrock's ConverseStream API.
//
// It deliberately covers only one API and a curated set of credential
// sources (env, shared profile, credential_process, AssumeRole chains,
// IRSA, ECS, IMDSv2). For everything else, use the official AWS SDK.
//
// See the project README and AGENTS.md for scope and tradeoffs.
package skipstone
