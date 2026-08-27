package awsbrowser

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
)

const GlobalRegion = "global"

var (
	ErrInvalidAWSContext     = errors.New("invalid or unverified AWS context")
	ErrInvalidResourceKey    = errors.New("invalid or unverified resource key")
	ErrInvalidQueryKey       = errors.New("invalid query key")
	ErrInvalidMappedFields   = errors.New("invalid mapped resource fields")
	ErrIncompleteObservation = errors.New("incomplete resource observation")
	ErrIncompletePage        = errors.New("incomplete query page")
)

// AWSContext is an identity-verified, credential-free AWS execution scope.
// Values must be created with NewAWSContext; copied values remain valid, while
// changing any exported provenance field makes validation fail.
type AWSContext struct {
	Profile       string
	Partition     string
	AccountID     string
	PrincipalARN  string
	RoleName      string
	Region        string
	Mode          ContextMode
	CredentialGen uint64

	seal string
}

// NewAWSContext binds profile provenance and region to an STS-verified
// identity. It deliberately accepts no credential material.
func NewAWSContext(spec ContextSpec, identity VerifiedIdentity, roleName string) (AWSContext, error) {
	if _, err := validateContextSpec(spec); err != nil || !validVerifiedIdentity(identity) ||
		containsControl(roleName) {
		return AWSContext{}, ErrInvalidAWSContext
	}
	context := AWSContext{
		Profile:       spec.Profile,
		Partition:     identity.Partition,
		AccountID:     identity.AccountID,
		PrincipalARN:  identity.PrincipalARN,
		RoleName:      roleName,
		Region:        spec.Region,
		Mode:          spec.Mode,
		CredentialGen: identity.CredentialGeneration,
	}
	context.seal = context.identitySeal()
	return context, nil
}

func validVerifiedIdentity(identity VerifiedIdentity) bool {
	if identity.CredentialGeneration == 0 || !accountIDRE.MatchString(identity.AccountID) ||
		!partitionRE.MatchString(identity.Partition) || identity.PrincipalARN == "" ||
		identityContainsForbiddenControl(identity.PrincipalARN) {
		return false
	}
	parsed, err := arn.Parse(identity.PrincipalARN)
	return err == nil && parsed.Partition == identity.Partition && parsed.AccountID == identity.AccountID &&
		parsed.Region == "" && parsed.Resource != "" && (parsed.Service == "iam" || parsed.Service == "sts")
}

func (context AWSContext) Validate() error {
	if context.seal == "" || context.seal != context.identitySeal() {
		return ErrInvalidAWSContext
	}
	identity := VerifiedIdentity{
		Partition:            context.Partition,
		AccountID:            context.AccountID,
		PrincipalARN:         context.PrincipalARN,
		CredentialGeneration: context.CredentialGen,
	}
	if _, err := validateContextSpec(ContextSpec{Mode: context.Mode, Profile: context.Profile, Region: context.Region}); err != nil ||
		!validVerifiedIdentity(identity) || containsControl(context.RoleName) {
		return ErrInvalidAWSContext
	}
	return nil
}

func (context AWSContext) identitySeal() string {
	return joinIdentityParts(
		string(context.Mode), context.Profile, context.Partition, context.AccountID,
		context.PrincipalARN, context.RoleName, context.Region,
		strconv.FormatUint(context.CredentialGen, 10),
	)
}

// ContextProvenance is a comparable key that preserves the profile and the
// exact verified credential generation used for an observation.
type ContextProvenance struct {
	Mode          ContextMode
	Profile       string
	Partition     string
	AccountID     string
	PrincipalARN  string
	RoleName      string
	Region        string
	CredentialGen uint64
}

func (context AWSContext) Provenance() (ContextProvenance, error) {
	if err := context.Validate(); err != nil {
		return ContextProvenance{}, err
	}
	return ContextProvenance{
		Mode:          context.Mode,
		Profile:       context.Profile,
		Partition:     context.Partition,
		AccountID:     context.AccountID,
		PrincipalARN:  context.PrincipalARN,
		RoleName:      context.RoleName,
		Region:        context.Region,
		CredentialGen: context.CredentialGen,
	}, nil
}

// ResourceKey is the canonical, account- and region-scoped identity of a
// resource. Use NewRegionalResourceKey or NewGlobalResourceKey so unverified
// account data cannot enter the canonical store.
type ResourceKey struct {
	Partition string
	AccountID string
	Region    string
	Type      string
	ID        string

	seal string
}

func NewRegionalResourceKey(context AWSContext, resourceType, id string) (ResourceKey, error) {
	return newResourceKey(context, context.Region, resourceType, id)
}

func NewGlobalResourceKey(context AWSContext, resourceType, id string) (ResourceKey, error) {
	return newResourceKey(context, GlobalRegion, resourceType, id)
}

func newResourceKey(context AWSContext, region, resourceType, id string) (ResourceKey, error) {
	if err := context.Validate(); err != nil || !validIdentifier(resourceType) || !validResourceID(id) ||
		(region != GlobalRegion && region != context.Region) {
		return ResourceKey{}, ErrInvalidResourceKey
	}
	key := ResourceKey{
		Partition: context.Partition,
		AccountID: context.AccountID,
		Region:    region,
		Type:      strings.TrimSpace(resourceType),
		ID:        strings.TrimSpace(id),
	}
	key.seal = key.identitySeal()
	return key, nil
}

func (key ResourceKey) Validate() error {
	if key.seal == "" || key.seal != key.identitySeal() || !partitionRE.MatchString(key.Partition) ||
		!accountIDRE.MatchString(key.AccountID) || (key.Region != GlobalRegion && !regionNameRE.MatchString(key.Region)) ||
		!validIdentifier(key.Type) || !validResourceID(key.ID) {
		return ErrInvalidResourceKey
	}
	return nil
}

func (key ResourceKey) identitySeal() string {
	return joinIdentityParts(key.Partition, key.AccountID, key.Region, key.Type, key.ID)
}

// QueryKey is normalized, comparable, and contains context provenance rather
// than credential material. ParamsKey is a deterministic URL-encoded form of
// explicit mapped query parameters.
type QueryKey struct {
	Context   AWSContext
	Provider  string
	Operation string
	ParamsKey string

	seal string
}

func NewQueryKey(context AWSContext, provider, operation string, params map[string]string) (QueryKey, error) {
	if err := context.Validate(); err != nil || !validIdentifier(provider) || !validIdentifier(operation) {
		return QueryKey{}, ErrInvalidQueryKey
	}
	paramsKey, err := normalizeQueryParams(params)
	if err != nil {
		return QueryKey{}, err
	}
	key := QueryKey{
		Context:   context,
		Provider:  strings.ToLower(strings.TrimSpace(provider)),
		Operation: strings.TrimSpace(operation),
		ParamsKey: paramsKey,
	}
	key.seal = key.identitySeal()
	return key, nil
}

func (key QueryKey) Validate() error {
	if err := key.Context.Validate(); err != nil || key.seal == "" || key.seal != key.identitySeal() ||
		!validIdentifier(key.Provider) || !validIdentifier(key.Operation) {
		return ErrInvalidQueryKey
	}
	params, err := url.ParseQuery(key.ParamsKey)
	if err != nil {
		return ErrInvalidQueryKey
	}
	normalized := make(map[string]string, len(params))
	for name, values := range params {
		if len(values) != 1 {
			return ErrInvalidQueryKey
		}
		normalized[name] = values[0]
	}
	canonical, err := normalizeQueryParams(normalized)
	if err != nil || canonical != key.ParamsKey {
		return ErrInvalidQueryKey
	}
	return nil
}

func (key QueryKey) identitySeal() string {
	return joinIdentityParts(key.Context.identitySeal(), key.Provider, key.Operation, key.ParamsKey)
}

func normalizeQueryParams(params map[string]string) (string, error) {
	if len(params) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(params))
	for name := range params {
		if strings.TrimSpace(name) != name || !validIdentifier(name) || sensitiveParamName(name) {
			return "", ErrInvalidQueryKey
		}
		names = append(names, name)
	}
	sort.Strings(names)
	values := make(url.Values, len(names))
	for _, name := range names {
		value := params[name]
		if len(value) > 4096 || containsControl(value) {
			return "", ErrInvalidQueryKey
		}
		values.Set(name, value)
	}
	return values.Encode(), nil
}

func sensitiveParamName(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(name))
	for _, fragment := range []string{"accesskey", "secret", "credential", "password", "authorization", "sessiontoken", "securitytoken"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return normalized == "token"
}

// ResourceObservation is one complete, profile-scoped mapped view of a
// resource. Fields returns a deep copy and never exposes the stored map.
type ResourceObservation struct {
	Context   AWSContext
	FetchedAt time.Time
	Complete  bool

	fields map[string]any
}

func NewResourceObservation(context AWSContext, fields map[string]any, fetchedAt time.Time, complete bool) (ResourceObservation, error) {
	if err := context.Validate(); err != nil || fetchedAt.IsZero() {
		return ResourceObservation{}, ErrIncompleteObservation
	}
	cloned, err := cloneMappedFields(fields)
	if err != nil {
		return ResourceObservation{}, err
	}
	return ResourceObservation{
		Context:   context,
		FetchedAt: fetchedAt.UTC(),
		Complete:  complete,
		fields:    cloned,
	}, nil
}

func (observation ResourceObservation) Fields() map[string]any {
	fields, _ := cloneMappedFields(observation.fields)
	return fields
}

func (observation ResourceObservation) clone() ResourceObservation {
	copy := observation
	copy.fields, _ = cloneMappedFields(observation.fields)
	return copy
}

func (observation ResourceObservation) validateComplete() error {
	if !observation.Complete || observation.FetchedAt.IsZero() || observation.Context.Validate() != nil {
		return ErrIncompleteObservation
	}
	if _, err := cloneMappedFields(observation.fields); err != nil {
		return err
	}
	return nil
}

// ObservedResource keeps a canonical key adjacent to the profile-scoped
// observation that produced it.
type ObservedResource struct {
	Key         ResourceKey
	Observation ResourceObservation
}

// QueryPage is a successfully decoded page candidate. CommitPage rejects it
// unless Complete is true, so cancelled, failed, or partially decoded pages
// cannot enter the store.
type QueryPage struct {
	Number    uint64
	FetchedAt time.Time
	Complete  bool

	resources []ObservedResource
}

func NewQueryPage(number uint64, resources []ObservedResource, fetchedAt time.Time, complete bool) (QueryPage, error) {
	if fetchedAt.IsZero() {
		return QueryPage{}, ErrIncompletePage
	}
	page := QueryPage{Number: number, FetchedAt: fetchedAt.UTC(), Complete: complete}
	page.resources = cloneObservedResources(resources)
	return page, nil
}

func (page QueryPage) Resources() []ObservedResource {
	return cloneObservedResources(page.resources)
}

func (page QueryPage) clone() QueryPage {
	copy := page
	copy.resources = cloneObservedResources(page.resources)
	return copy
}

func cloneObservedResources(resources []ObservedResource) []ObservedResource {
	if resources == nil {
		return nil
	}
	result := make([]ObservedResource, len(resources))
	for index, resource := range resources {
		result[index] = ObservedResource{Key: resource.Key, Observation: resource.Observation.clone()}
	}
	return result
}

func cloneMappedFields(fields map[string]any) (map[string]any, error) {
	if fields == nil {
		return map[string]any{}, nil
	}
	result := make(map[string]any, len(fields))
	for name, value := range fields {
		if strings.TrimSpace(name) != name || !validIdentifier(name) || sensitiveParamName(name) {
			return nil, ErrInvalidMappedFields
		}
		cloned, err := cloneMappedValue(value)
		if err != nil {
			return nil, err
		}
		result[name] = cloned
	}
	return result, nil
}

func cloneMappedValue(value any) (any, error) {
	switch value := value.(type) {
	case nil, bool, string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64, time.Time:
		return value, nil
	case ResourceKey:
		if err := value.Validate(); err != nil {
			return nil, ErrInvalidMappedFields
		}
		return value, nil
	case []string:
		return append([]string(nil), value...), nil
	case []ResourceKey:
		for _, key := range value {
			if err := key.Validate(); err != nil {
				return nil, ErrInvalidMappedFields
			}
		}
		return append([]ResourceKey(nil), value...), nil
	case map[string]string:
		copy := make(map[string]string, len(value))
		for key, item := range value {
			copy[key] = item
		}
		return copy, nil
	case []any:
		copy := make([]any, len(value))
		for index, item := range value {
			cloned, err := cloneMappedValue(item)
			if err != nil {
				return nil, err
			}
			copy[index] = cloned
		}
		return copy, nil
	case map[string]any:
		return cloneMappedFields(value)
	default:
		return nil, fmt.Errorf("%w: unsupported value type %T", ErrInvalidMappedFields, value)
	}
}

func validIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !containsControl(value)
}

func validResourceID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 2048 && !containsControl(value)
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func joinIdentityParts(parts ...string) string {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteByte(':')
		builder.WriteString(part)
	}
	return builder.String()
}
