package awsbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type IntentKind string

const (
	IntentOpen    IntentKind = "open"
	IntentRefresh IntentKind = "refresh"
	IntentSearch  IntentKind = "cross-profile-search"
)

// Intent is a credential-free request from a browser view. Target is either a
// catalog route or an opaque relation target understood by the integration
// layer; the TUI never turns it into provider-specific input itself.
type Intent struct {
	Kind       IntentKind
	Target     string
	Profile    string
	Region     string
	SearchKind string
	Query      string
	Scope      string
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
	Kind       string
	Reason     string
	Scope      string
	Operation  string
	ObservedAt string
}

type ResourceProjection struct {
	Target    string
	Title     string
	Subtitle  string
	Fields    []ProjectionField
	Relations []ProjectionRelation
}

type IntentProjection struct {
	Resources []ResourceProjection
}

// IntentUpdate is one progressive stream item. Query carries the exact store
// snapshot and typed failure; Context is populated once identity resolution
// succeeds. Done is optional because closing the stream is also terminal.
type IntentUpdate struct {
	Context    *AWSContext
	Query      QueryUpdate
	Projection IntentProjection
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
			projection := ResourceProjection{
				Target:   observed.Key.Type + ":" + observed.Key.ID,
				Title:    projectionTitle(observed.Key, fields),
				Subtitle: projectionSubtitle(fields),
			}
			projection.Fields = projectFields(fields)
			projection.Relations = projectRelations(fields)
			resources = append(resources, projection)
		}
	}
	return IntentProjection{Resources: resources}
}

func projectionTitle(key ResourceKey, fields map[string]any) string {
	for _, name := range []string{"name", "role_name", "dns_name", "record_name"} {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return safeIntentText(value) + " · " + safeIntentText(key.ID)
		}
	}
	return safeIntentText(key.ID)
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
		if name != "relations" && name != "alias_relation" && name != "zone_relation" {
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
		values = append(values, namedRelation{name: "Alias target", value: relation})
	}
	if relation, ok := fields["zone_relation"].(map[string]any); ok {
		values = append(values, namedRelation{name: "Hosted zone", value: relation})
	}
	result := make([]ProjectionRelation, 0, len(values))
	for _, raw := range values {
		label := ""
		if named, ok := raw.(namedRelation); ok {
			label, raw = named.name, named.value
		}
		relation, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		target, hasTarget := relation["target"].(ResourceKey)
		kind, _ := relation["kind"].(string)
		reason, _ := relation["reason"].(string)
		item := ProjectionRelation{
			Label: label, Kind: safeIntentText(kind), Reason: safeIntentText(reason),
			Scope: safeRelationString(relation["scope"]), Operation: safeRelationString(relation["operation"]),
			ObservedAt: relationTime(relation["observed_at"]),
		}
		if hasTarget && target.Validate() == nil {
			item.Target = target.Type + ":" + target.ID
			if item.Label == "" {
				item.Label = target.ID
			}
		} else if item.Label == "" {
			item.Label = "External target"
		}
		if item.Reason == "" {
			item.Reason = "relationship evidence available"
		}
		result = append(result, item)
	}
	return result
}

type namedRelation struct {
	name  string
	value map[string]any
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
