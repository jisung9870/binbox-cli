package awsbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	contextProfileRE           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	contextRegionRE            = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+-[0-9]+$`)
	errInvalidContextSelection = errors.New("invalid AWS context selection")
)

// ValidateContextSelection is the shared CLI/integration boundary for an
// optional explicit profile and region. Empty values select ambient/default
// resolution; non-empty values must already be canonical and bounded.
func ValidateContextSelection(profile, region string) error {
	if profile != strings.TrimSpace(profile) || region != strings.TrimSpace(region) ||
		(profile != "" && !contextProfileRE.MatchString(profile)) ||
		(region != "" && (len(region) > 64 || !contextRegionRE.MatchString(region))) {
		return errInvalidContextSelection
	}
	return nil
}

type IntentKind string

const (
	IntentOpen     IntentKind = "open"
	IntentRefresh  IntentKind = "refresh"
	IntentSearch   IntentKind = "cross-profile-search"
	IntentIncoming IntentKind = "incoming-relations"
)

// Intent is a credential-free request from a browser view. Target is either a
// catalog route or an opaque relation target understood by the integration
// layer; the TUI never turns it into provider-specific input itself.
type Intent struct {
	Kind       IntentKind
	Target     string
	Profile    string
	Region     string
	Regions    string
	SearchKind string
	Query      string
	Scope      string
	// ExpectedPartition and ExpectedAccountID fence snapshot-backed reverse
	// lookups to the exact live resource identity shown in Summary.
	ExpectedPartition string
	ExpectedAccountID string
	Force             bool
}

// ProjectionField and ProjectionRelation are safe, provider-independent view
// values. Integrations may supply a tailored Projection, or leave it empty and
// let ProjectQueryUpdate derive a deterministic generic view from mapped store
// observations.
type ProjectionField struct {
	Label string
	Value string
}

type ProjectionRelation struct {
	Label      string
	Target     string
	TargetRef  string
	Type       string
	Direction  string
	Condition  string
	Kind       string
	Reason     string
	Scope      string
	Operation  string
	ObservedAt string
}

type ProjectionTag struct {
	Key   string
	Value string
}

type ResourceProjection struct {
	Target    string
	Title     string
	Subtitle  string
	Fields    []ProjectionField
	Relations []ProjectionRelation
	Tags      []ProjectionTag
	// Context is the exact credential-free execution context that produced this
	// resource. It is per-resource because cross-profile results may span
	// accounts; callers must not promote it to one list-wide context.
	Context              *AWSContext
	Current              bool
	AvailableViaProfiles []string
}

type IntentProjection struct {
	Resources []ResourceProjection
}

// SearchProfileCoverage and SearchCoverage are integration-independent,
// credential-free DTOs for interactive search. String statuses are sanitized
// at the integration boundary and intentionally carry no provider message.
type SearchProfileCoverage struct {
	Profile   string
	Region    string
	AccountID string
	Status    string
	Current   bool
	Matches   int
}

type SearchCoverage struct {
	DiscoveryStatus string
	Profiles        []SearchProfileCoverage
	Partial         bool
}

// GraphSnapshot identifies snapshot-backed relationship results. It is kept
// separate from QuerySnapshot so the TUI never presents a graph cache as a
// live provider response.
type GraphSnapshot struct {
	Group       string
	CompletedAt time.Time
	AgeSeconds  int64
	Succeeded   int
	Failed      int
	NotObserved int
	Reused      bool
	Collecting  bool
	Error       bool
}

// IntentUpdate is one progressive stream item. Query carries the exact store
// snapshot and typed failure; Context is populated once identity resolution
// succeeds. Done is optional because closing the stream is also terminal.
type IntentUpdate struct {
	Context    *AWSContext
	Query      QueryUpdate
	Projection IntentProjection
	Coverage   *SearchCoverage
	Graph      *GraphSnapshot
	Done       bool
}

// IntentStream has single-owner cancellation semantics. Cancel must be safe to
// call repeatedly and must eventually stop or detach the producer.
type IntentStream interface {
	Updates() <-chan IntentUpdate
	Cancel()
}

// ChannelIntentStream is the small adapter integrations and tests can use to
// expose an update channel without importing Bubble Tea.
type ChannelIntentStream struct {
	C          <-chan IntentUpdate
	CancelFunc func()
	once       sync.Once
}

func (stream *ChannelIntentStream) Updates() <-chan IntentUpdate {
	if stream == nil {
		return nil
	}
	return stream.C
}

func (stream *ChannelIntentStream) Cancel() {
	if stream != nil {
		stream.once.Do(func() {
			if stream.CancelFunc != nil {
				stream.CancelFunc()
			}
		})
	}
}

type IntentDispatcher interface {
	Dispatch(context.Context, Intent) (IntentStream, error)
}

// ContextChoice is one credential-free configured profile candidate. Region is
// a non-secret default read from shared AWS configuration and remains editable
// before verification.
type ContextChoice struct {
	Profile string
	Region  string
	Group   string
	Regions []string
}

// ContextResolution contains only verified identity metadata or one sanitized
// typed failure. Account and principal are always derived from the selected
// profile's credentials; callers never supply either value.
type ContextResolution struct {
	Context *AWSContext
	Failure *ProviderFailure
}

// ContextCatalog is an optional browser capability. Listing is local-only;
// resolving is an explicit credential and STS identity operation.
type ContextCatalog interface {
	ListContexts(context.Context) ([]ContextChoice, error)
	ResolveContext(context.Context, string, string) (ContextResolution, error)
}

// IntentResultMsg is emitted by the first async command. Subsequent stream
// items are wrapped internally by Model so late messages can be generation
// fenced after Back, refresh, or route replacement.
type IntentResultMsg struct {
	Intent Intent
	Stream IntentStream
	Err    error
}

func (m IntentResultMsg) Error() string {
	if m.Err == nil {
		return ""
	}
	return safeIntentText(fmt.Sprintf("%s: %v", m.Intent.Target, m.Err))
}

func safeIntentText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x1b {
			return ' '
		}
		return r
	}, value)
}

// ProjectQueryUpdate exposes only complete mapped observations already
// admitted to the SessionStore. Provider structs and raw SDK values never
// enter this projection boundary.
func ProjectQueryUpdate(update QueryUpdate) IntentProjection {
	resources := make([]ResourceProjection, 0, update.Snapshot.ResourceCount())
	for _, page := range update.Snapshot.Pages() {
		for _, observed := range page.Resources() {
			fields := observed.Observation.Fields()
			projection := ProjectResourceFields(observed.Key, fields)
			resources = append(resources, projection)
		}
	}
	return IntentProjection{Resources: resources}
}

// ProjectResourceFields applies the same sanitized, provider-independent
// projection boundary to canonical search resources and streamed query pages.
func ProjectResourceFields(key ResourceKey, fields map[string]any) ResourceProjection {
	title := projectionTitle(key, fields)
	detail := projectionSubtitle(fields)
	subtitle := detail
	if title != safeIntentText(key.ID) {
		subtitle = safeIntentText(key.ID)
		if detail != "" {
			subtitle += " · " + detail
		}
	}
	projectedFields := projectFields(fields)
	relations := withoutSelfRelations(key, projectRelations(fields))
	tags := projectTags(fields)
	switch key.Type {
	case "ec2.security-group":
		projectedFields = withoutProjectionField(projectedFields, "Rules")
		relations = append(relations,
			ProjectionRelation{
				Label: "Inbound rules", Target: "ec2.security-group-rules-inbound:" + key.ID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Kind: "scoped-query", Reason: "security group inbound rules", Scope: key.Region,
			},
			ProjectionRelation{
				Label: "Outbound rules", Target: "ec2.security-group-rules-outbound:" + key.ID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Kind: "scoped-query", Reason: "security group outbound rules", Scope: key.Region,
			},
		)
	case "ec2.security-group-rule":
		title = securityGroupRuleTitle(fields)
		subtitle = safeIntentText(key.ID)
		if description, ok := fields["description"].(string); ok && strings.TrimSpace(description) != "" {
			subtitle += " · " + safeIntentText(description)
		}
	case "iam.role":
		relations = append(relations,
			ProjectionRelation{
				Label: "Attached policies", Target: "iam.role-attached-policies:" + key.ID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Kind: "scoped-query", Reason: "managed policies attached to role", Scope: GlobalRegion,
			},
			ProjectionRelation{
				Label: "Inline policies", Target: "iam.role-inline-policies:" + key.ID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Kind: "scoped-query", Reason: "inline policies embedded in role", Scope: GlobalRegion,
			},
		)
	case "iam.managed-policy":
		if versionID, ok := fields["default_version_id"].(string); ok && strings.TrimSpace(versionID) != "" {
			relations = append(relations, ProjectionRelation{
				Label: "Default policy document", Target: "iam.managed-policy-version:" + key.ID + ":" + versionID,
				Type: string(RelationHasVersion), Direction: string(RelationOutgoing), Condition: safeIntentText(versionID),
				Kind: "scoped-query", Reason: "managed policy default version document", Scope: GlobalRegion,
			})
		}
	case "iam.managed-policy-version":
		policyARN, _ := fields["policy_arn"].(string)
		versionID, _ := fields["version_id"].(string)
		policyName := policyARN
		if index := strings.LastIndex(policyARN, "/"); index >= 0 && index < len(policyARN)-1 {
			policyName = policyARN[index+1:]
		}
		if strings.TrimSpace(policyName) != "" && strings.TrimSpace(versionID) != "" {
			title = safeIntentText(policyName + " · " + versionID)
			subtitle = safeIntentText(policyARN)
		}
	case "hosted-zone":
		relations = append(relations, ProjectionRelation{
			Label: "DNS records", Target: "route53.records:" + key.ID,
			Type: string(RelationContains), Direction: string(RelationOutgoing),
			Kind: "scoped-query", Reason: "record sets in hosted zone", Scope: GlobalRegion,
		})
	case "elbv2.load-balancer":
		relations = append(relations, ProjectionRelation{
			Label: "Listeners", Target: "elbv2.listeners:" + key.ID,
			Type: string(RelationContains), Direction: string(RelationOutgoing),
			Kind: "scoped-query", Reason: "listeners attached to load balancer", Scope: key.Region,
		})
	case "elbv2.listener":
		if strings.Contains(key.ID, ":listener/app/") {
			relations = append(relations, ProjectionRelation{
				Label: "Listener rules", Target: "elbv2.rules:" + key.ID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Kind: "scoped-query", Reason: "ordered rules attached to application load balancer listener", Scope: key.Region,
			})
		}
	case "elbv2.target-group":
		if targetType, ok := fields["target_type"].(string); ok && strings.TrimSpace(targetType) != "" {
			targetID := url.Values{"target-group-arn": []string{key.ID}, "target-type": []string{targetType}}.Encode()
			relations = append(relations, ProjectionRelation{
				Label: "Registered targets", Target: "elbv2.targets:" + targetID,
				Type: string(RelationContains), Direction: string(RelationOutgoing),
				Condition: "target-type=" + safeIntentText(targetType), Kind: "scoped-query",
				Reason: "target health for target group", Scope: key.Region,
			})
		}
	}
	return ResourceProjection{
		Target: key.Type + ":" + key.ID, Title: title, Subtitle: subtitle,
		Fields: projectedFields, Relations: relations, Tags: tags,
	}
}

func withoutSelfRelations(key ResourceKey, relations []ProjectionRelation) []ProjectionRelation {
	target := key.Type + ":" + key.ID
	result := make([]ProjectionRelation, 0, len(relations))
	for _, relation := range relations {
		if relation.Target != target {
			result = append(result, relation)
		}
	}
	return result
}

func withoutProjectionField(fields []ProjectionField, label string) []ProjectionField {
	result := make([]ProjectionField, 0, len(fields))
	for _, field := range fields {
		if field.Label != label {
			result = append(result, field)
		}
	}
	return result
}

func securityGroupRuleTitle(fields map[string]any) string {
	protocol, _ := fields["protocol"].(string)
	protocol = strings.ToUpper(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "Rule"
	} else if protocol == "-1" {
		protocol = "All traffic"
	}
	from, fromOK := fields["from_port"].(int32)
	to, toOK := fields["to_port"].(int32)
	if protocol != "All traffic" && fromOK && toOK {
		if from == to {
			protocol += fmt.Sprintf(" %d", from)
		} else {
			protocol += fmt.Sprintf(" %d–%d", from, to)
		}
	}
	peer := firstProjectionString(fields, "cidr_ipv4", "cidr_ipv6", "prefix_list_id")
	if peer == "" {
		if reference, ok := fields["referenced_group"].(map[string]any); ok {
			peer, _ = reference["group_id"].(string)
		}
	}
	if strings.TrimSpace(peer) != "" {
		return protocol + " · " + safeIntentText(peer)
	}
	return protocol
}

func firstProjectionString(fields map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func projectionTitle(key ResourceKey, fields map[string]any) string {
	if tags, ok := fields["tags"].(map[string]string); ok {
		if value := strings.TrimSpace(tags["Name"]); value != "" {
			return safeIntentText(value)
		}
	}
	for _, name := range []string{"name", "target_id", "role_name", "instance_profile_name", "policy_name", "dns_name", "record_name", "domain_name", "bucket_name", "distribution_id"} {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return safeIntentText(value)
		}
	}
	return safeIntentText(key.ID)
}

func projectTags(fields map[string]any) []ProjectionTag {
	tags, ok := fields["tags"].(map[string]string)
	if !ok || len(tags) == 0 {
		return nil
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ProjectionTag, 0, len(keys))
	for _, key := range keys {
		result = append(result, ProjectionTag{Key: safeIntentText(key), Value: safeIntentText(tags[key])})
	}
	return result
}

func projectionSubtitle(fields map[string]any) string {
	for _, name := range []string{"state", "type", "instance_type", "description"} {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return safeIntentText(value)
		}
	}
	return ""
}

func projectFields(fields map[string]any) []ProjectionField {
	names := make([]string, 0, len(fields))
	for name := range fields {
		if name != "relations" && name != "alias_relation" && name != "zone_relation" && name != "tags" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result := make([]ProjectionField, 0, len(names))
	for _, name := range names {
		if value, ok := projectionValue(fields[name]); ok {
			result = append(result, ProjectionField{Label: humanLabel(name), Value: value})
		}
	}
	return result
}

func projectionValue(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return safeIntentText(value), value != ""
	case bool:
		return fmt.Sprintf("%t", value), true
	case int:
		return fmt.Sprintf("%d", value), true
	case int32:
		return fmt.Sprintf("%d", value), true
	case int64:
		return fmt.Sprintf("%d", value), true
	case uint:
		return fmt.Sprintf("%d", value), true
	case uint32:
		return fmt.Sprintf("%d", value), true
	case uint64:
		return fmt.Sprintf("%d", value), true
	case float32:
		return fmt.Sprintf("%g", value), true
	case float64:
		return fmt.Sprintf("%g", value), true
	case time.Time:
		return value.UTC().Format(time.RFC3339), !value.IsZero()
	case ResourceKey:
		return safeIntentText(value.Type + ":" + value.ID), value.Validate() == nil
	case []ResourceKey:
		parts := make([]string, 0, len(value))
		for _, key := range value {
			if key.Validate() == nil {
				parts = append(parts, safeIntentText(key.Type+":"+key.ID))
			}
		}
		return strings.Join(parts, ", "), len(parts) != 0
	case []string:
		parts := make([]string, len(value))
		for index := range value {
			parts[index] = safeIntentText(value[index])
		}
		return strings.Join(parts, ", "), len(parts) != 0
	case map[string]string:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, safeIntentText(key)+"="+safeIntentText(value[key]))
		}
		return strings.Join(parts, ", "), len(parts) != 0
	case []any:
		return projectionJSON(value)
	case map[string]any:
		return projectionJSON(value)
	default:
		return "", false
	}
}

func projectionJSON(value any) (string, bool) {
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "[]" || string(encoded) == "{}" || string(encoded) == "null" {
		return "", false
	}
	return safeIntentText(string(encoded)), true
}

func projectRelations(fields map[string]any) []ProjectionRelation {
	values := make([]any, 0)
	if relations, ok := fields["relations"].([]any); ok {
		values = append(values, relations...)
	}
	if relation, ok := fields["alias_relation"].(map[string]any); ok {
		targetHint := ""
		if alias, aliasOK := fields["alias"].(map[string]any); aliasOK {
			targetHint = safeRelationString(alias["dns_name"])
		}
		values = append(values, namedRelation{name: "Alias target", value: relation, targetHint: targetHint})
	}
	if relation, ok := fields["zone_relation"].(map[string]any); ok {
		values = append(values, namedRelation{name: "Hosted zone", value: relation})
	}
	result := make([]ProjectionRelation, 0, len(values))
	for _, raw := range values {
		label, targetHint := "", ""
		if named, ok := raw.(namedRelation); ok {
			label, raw = named.name, named.value
			targetHint = named.targetHint
		}
		relation, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if label == "" {
			label, _ = relation["label"].(string)
		}
		target, hasTarget := relation["target"].(ResourceKey)
		kind, _ := relation["kind"].(string)
		reason, _ := relation["reason"].(string)
		item := ProjectionRelation{
			Label: label, Type: safeRelationString(relation["relation_type"]),
			Direction: safeRelationString(relation["direction"]), Condition: safeRelationString(relation["condition"]),
			Kind: safeIntentText(kind), Reason: safeIntentText(reason),
			Scope: safeRelationString(relation["scope"]), Operation: safeRelationString(relation["operation"]),
			ObservedAt: relationTime(relation["observed_at"]),
		}
		if hasTarget && target.Validate() == nil {
			item.TargetRef = target.Type + ":" + target.ID
			if NavigableRelationTargetType(target.Type) {
				item.Target = item.TargetRef
			}
			if item.Label == "" {
				item.Label = target.ID
			}
		} else if item.Label == "" {
			item.Label = "External target"
		}
		if item.TargetRef == "" {
			item.TargetRef = targetHint
		}
		if item.Reason == "" {
			item.Reason = "relationship evidence available"
		}
		result = append(result, item)
	}
	return result
}

// NavigableRelationTargetType is the frozen production binding contract. A
// valid exact relation remains visible when false, but is evidence-only until a
// narrowed read operation is implemented for its resource type.
func NavigableRelationTargetType(resourceType string) bool {
	switch resourceType {
	case "ec2.instance", "ec2.volume", "ec2.security-group", "ec2.security-group-rule",
		"ec2.security-group-rules-inbound", "ec2.security-group-rules-outbound",
		"ec2.vpc", "ec2.subnet", "ec2.route-table", "iam.role", "iam.instance-profile",
		"iam.role-attached-policies", "iam.role-inline-policies", "iam.managed-policy", "iam.inline-policy",
		"iam.managed-policy-version", "hosted-zone", "route53.records",
		"cloudfront.distribution-domain", "elbv2.load-balancer-dns", "elbv2.load-balancer",
		"elbv2.listeners", "elbv2.rules", "elbv2.target-group", "elbv2.targets", "s3.bucket":
		return true
	default:
		return false
	}
}

type namedRelation struct {
	name       string
	value      map[string]any
	targetHint string
}

func safeRelationString(value any) string {
	text, _ := value.(string)
	return safeIntentText(text)
}

func relationTime(value any) string {
	when, ok := value.(time.Time)
	if !ok || when.IsZero() {
		return ""
	}
	return when.UTC().Format(time.RFC3339)
}

func humanLabel(value string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(value))
	for index := range words {
		if words[index] != "" {
			words[index] = strings.ToUpper(words[index][:1]) + words[index][1:]
		}
	}
	return strings.Join(words, " ")
}
