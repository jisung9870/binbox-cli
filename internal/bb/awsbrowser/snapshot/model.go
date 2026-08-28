package snapshot

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

var (
	ErrInvalidInput   = errors.New("invalid snapshot input")
	ErrNoActiveRun    = errors.New("no active snapshot run")
	ErrTraversalLimit = errors.New("snapshot graph traversal limit exceeded")
	ErrCorruptStore   = errors.New("corrupt snapshot store")
	ErrSchemaVersion  = errors.New("unsupported snapshot schema version")
)

const (
	SchemaVersion = 2
	GlobalRegion  = awsbrowser.GlobalRegion
)

var (
	partitionPattern = regexp.MustCompile(`^aws(?:-[a-z0-9-]+)?$`)
	accountPattern   = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern    = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z0-9-]+-[0-9]+$`)
)

type CoverageStatus string

const (
	CoverageSucceeded   CoverageStatus = "succeeded"
	CoverageFailed      CoverageStatus = "failed"
	CoverageNotObserved CoverageStatus = "not-observed"
)

func (status CoverageStatus) valid() bool {
	switch status {
	case CoverageSucceeded, CoverageFailed, CoverageNotObserved:
		return true
	default:
		return false
	}
}

// ResourceRef is the persistent identity boundary. It mirrors the browser's
// partition/account/region/type/id key without storing credentials or SDK data.
type ResourceRef struct {
	Partition string
	AccountID string
	Region    string
	Type      string
	ID        string
}

func RefFromKey(key awsbrowser.ResourceKey) (ResourceRef, error) {
	if err := key.Validate(); err != nil {
		return ResourceRef{}, ErrInvalidInput
	}
	return ResourceRef{
		Partition: key.Partition,
		AccountID: key.AccountID,
		Region:    key.Region,
		Type:      key.Type,
		ID:        key.ID,
	}, nil
}

func (ref ResourceRef) Validate() error {
	if !partitionPattern.MatchString(ref.Partition) || !accountPattern.MatchString(ref.AccountID) ||
		(ref.Region != GlobalRegion && !regionPattern.MatchString(ref.Region)) ||
		!validText(ref.Type, 256) || !validText(ref.ID, 2048) {
		return ErrInvalidInput
	}
	return nil
}

func (ref ResourceRef) Key() (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("account", ref.AccountID)
	values.Set("id", ref.ID)
	values.Set("partition", ref.Partition)
	values.Set("region", ref.Region)
	values.Set("type", ref.Type)
	return values.Encode(), nil
}

func ParseResourceRefKey(value string) (ResourceRef, error) {
	values, err := url.ParseQuery(value)
	if err != nil || len(values) != 5 {
		return ResourceRef{}, ErrInvalidInput
	}
	read := func(name string) (string, bool) {
		items, ok := values[name]
		returnValue := ""
		if ok && len(items) == 1 {
			returnValue = items[0]
		}
		return returnValue, ok && len(items) == 1
	}
	accountID, accountOK := read("account")
	id, idOK := read("id")
	partition, partitionOK := read("partition")
	region, regionOK := read("region")
	resourceType, typeOK := read("type")
	if !accountOK || !idOK || !partitionOK || !regionOK || !typeOK {
		return ResourceRef{}, ErrInvalidInput
	}
	ref := ResourceRef{Partition: partition, AccountID: accountID, Region: region, Type: resourceType, ID: id}
	canonical, err := ref.Key()
	if err != nil || canonical != value {
		return ResourceRef{}, ErrInvalidInput
	}
	return ref, nil
}

type Resource struct {
	Ref  ResourceRef
	Name string
}

func (resource Resource) validate() error {
	if err := resource.Ref.Validate(); err != nil || !validOptionalText(resource.Name, 1024) {
		return ErrInvalidInput
	}
	return nil
}

type Observation struct {
	Resource   ResourceRef
	Profile    string
	AccountID  string
	Region     string
	ObservedAt time.Time
}

func (observation Observation) validate() error {
	if observation.Resource.Validate() != nil || !validText(observation.Profile, 256) ||
		!accountPattern.MatchString(observation.AccountID) || !validRegion(observation.Region) || observation.ObservedAt.IsZero() {
		return ErrInvalidInput
	}
	return nil
}

type Coverage struct {
	Profile   string
	AccountID string
	Region    string
	Service   string
	Status    CoverageStatus
	ErrorKind string
}

func (coverage Coverage) validate() error {
	accountValid := accountPattern.MatchString(coverage.AccountID) || coverage.Status != CoverageSucceeded && coverage.AccountID == ""
	if !validText(coverage.Profile, 256) || !accountValid ||
		!validRegion(coverage.Region) || !validText(coverage.Service, 128) ||
		!coverage.Status.valid() || !validOptionalText(coverage.ErrorKind, 256) {
		return ErrInvalidInput
	}
	if coverage.Status == CoverageSucceeded && coverage.ErrorKind != "" {
		return ErrInvalidInput
	}
	return nil
}

// Relation persists provider-independent semantics and evidence only. Reverse
// queries use Target's index; callers must not manufacture incoming copies.
type Relation struct {
	Source     ResourceRef
	Target     ResourceRef
	Type       awsbrowser.RelationType
	Direction  awsbrowser.RelationDirection
	Confidence awsbrowser.RelationKind
	Condition  string
	Reason     string
	Operation  string
	Scope      string
	ObservedAt time.Time
	Profile    string
	AccountID  string
	Region     string
}

func RelationsFromBrowser(relation awsbrowser.Relation, profile string) ([]Relation, error) {
	source, err := RefFromKey(relation.Source)
	if err != nil {
		return nil, err
	}
	target, err := RefFromKey(relation.Target)
	if err != nil {
		return nil, err
	}
	evidence := relation.Evidence()
	if len(evidence) == 0 {
		return nil, ErrInvalidInput
	}
	result := make([]Relation, len(evidence))
	for index, item := range evidence {
		result[index] = Relation{
			Source: source, Target: target, Type: relation.Semantics.Type, Direction: relation.Semantics.Direction,
			Confidence: item.Kind, Condition: relation.Semantics.Condition, Reason: item.Reason,
			Operation: item.Operation, Scope: item.Scope, ObservedAt: item.ObservedAt,
			Profile: profile, AccountID: source.AccountID, Region: source.Region,
		}
	}
	return result, nil
}

func (relation Relation) validate() error {
	if relation.Source.Validate() != nil || relation.Target.Validate() != nil || relation.ObservedAt.IsZero() ||
		!validOptionalText(relation.Condition, 4096) || !validText(relation.Reason, 1024) ||
		!validText(relation.Operation, 256) || !validText(relation.Scope, 128) ||
		!validText(relation.Profile, 256) || !accountPattern.MatchString(relation.AccountID) || !validRegion(relation.Region) {
		return ErrInvalidInput
	}
	semantics, err := awsbrowser.NewRelationSemantics(relation.Type, relation.Direction, relation.Condition)
	if err != nil {
		return ErrInvalidInput
	}
	evidence, err := awsbrowser.NewRelationEvidence(
		relation.Confidence,
		relation.Reason,
		relation.Operation,
		relation.Scope,
		relation.ObservedAt,
	)
	if err != nil {
		return ErrInvalidInput
	}
	if semantics.Direction != awsbrowser.RelationOutgoing || evidence.Kind == awsbrowser.RelationUnsupported {
		return ErrInvalidInput
	}
	return nil
}

type RunInput struct {
	StartedAt    time.Time
	CompletedAt  time.Time
	Resources    []Resource
	Observations []Observation
	Relations    []Relation
	Coverage     []Coverage
}

func (input RunInput) validate() error {
	if input.StartedAt.IsZero() || input.CompletedAt.IsZero() || input.CompletedAt.Before(input.StartedAt) || len(input.Coverage) == 0 {
		return ErrInvalidInput
	}
	for _, resource := range input.Resources {
		if err := resource.validate(); err != nil {
			return err
		}
	}
	for _, observation := range input.Observations {
		if err := observation.validate(); err != nil {
			return err
		}
	}
	for _, relation := range input.Relations {
		if err := relation.validate(); err != nil {
			return err
		}
	}
	coverageKeys := make(map[string]struct{}, len(input.Coverage))
	for _, coverage := range input.Coverage {
		if err := coverage.validate(); err != nil {
			return err
		}
		key := strings.Join([]string{coverage.Profile, coverage.AccountID, coverage.Region, coverage.Service}, "\x00")
		if _, exists := coverageKeys[key]; exists {
			return ErrInvalidInput
		}
		coverageKeys[key] = struct{}{}
	}
	return nil
}

type Run struct {
	ID            string
	StartedAt     time.Time
	CompletedAt   time.Time
	SchemaVersion int
}

type Edge struct {
	RunID      string
	SourceKey  string
	TargetKey  string
	Relation   Relation
	SourceName string
	TargetName string
	Observers  []Observer
}

type Observer struct {
	Profile    string
	AccountID  string
	Region     string
	ObservedAt time.Time
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= limit && !containsControl(value)
}

func validOptionalText(value string, limit int) bool {
	return value == "" || validText(value, limit)
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func validRegion(region string) bool {
	return region == GlobalRegion || regionPattern.MatchString(region)
}
