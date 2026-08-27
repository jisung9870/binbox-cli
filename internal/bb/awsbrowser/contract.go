package awsbrowser

import "context"

// ContextMode identifies how the browser resolves credentials for a context.
type ContextMode string

const (
	// ContextModeAmbient resolves the current shell context without a named
	// profile argument.
	ContextModeAmbient ContextMode = "ambient"
	// ContextModeNamedProfile resolves an explicitly named AWS CLI profile.
	ContextModeNamedProfile ContextMode = "named-profile"
)

// ContextSpec is the credential-free input used to create an AWS runtime.
type ContextSpec struct {
	Mode    ContextMode
	Profile string
	Region  string
}

// VerifiedIdentity binds an AWS identity to the credential generation that
// STS verified. It contains no credential material.
type VerifiedIdentity struct {
	Partition            string
	AccountID            string
	PrincipalARN         string
	CredentialGeneration uint64
}

// RuntimeFactory resolves a requested context into an identity-verified,
// read-only AWS runtime.
type RuntimeFactory interface {
	Resolve(context.Context, ContextSpec) (RuntimeContext, error)
}

// RuntimeContext exposes identity provenance and only the narrowed read
// clients required by AWS browser providers. Implementations retain ownership
// of SDK configuration, credential providers, and credential caches.
type RuntimeContext interface {
	Identity() VerifiedIdentity
	STS() STSAPI
	EC2() EC2API
	IAM() IAMAPI
	Route53() Route53API
}
