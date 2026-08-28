package providers

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const (
	hostedZoneType    = "hosted-zone"
	recordSetType     = "resource-record-set"
	cloudFrontZoneID  = "Z2FDTNDATAQYW2"
	zonePageSize      = int32(100)
	recordSetPageSize = int32(300)
)

type route53Executor struct {
	client awsbrowser.Route53API
	clock  func() time.Time
}

// NewRoute53 constructs the credential-free, read-only Route 53 executor.
func NewRoute53(client awsbrowser.Route53API, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	if nilInterface(client) || clock == nil {
		return nil, errors.New("route53 provider requires a client and clock")
	}
	return &route53Executor{client: client, clock: clock}, nil
}

func (executor *route53Executor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if ctx == nil {
		return awsbrowser.ErrInvalidQueryKey
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sink == nil || key.Validate() != nil {
		return awsbrowser.ErrInvalidQueryKey
	}
	if key.Provider != awsbrowser.ProviderRoute53 || awsbrowser.ValidateProviderOperation(key.Provider, key.Operation) != nil {
		return awsbrowser.ErrInvalidProviderOperation
	}
	params, err := parseRoute53Params(key)
	if err != nil {
		return err
	}
	switch key.Operation {
	case awsbrowser.OperationListHostedZones:
		err = executor.listHostedZones(ctx, key, sink)
	case awsbrowser.OperationListHostedZonesByName:
		err = executor.listHostedZonesByName(ctx, key, sink, params)
	case awsbrowser.OperationListResourceRecordSets:
		err = executor.listResourceRecordSets(ctx, key, sink, params)
	default:
		return awsbrowser.ErrInvalidProviderOperation
	}
	return err
}

func parseRoute53Params(key awsbrowser.QueryKey) (map[string]string, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil {
		return nil, awsbrowser.ErrInvalidQueryKey
	}
	params := make(map[string]string, len(values))
	allowed := map[string]bool{}
	switch key.Operation {
	case awsbrowser.OperationListHostedZones:
	case awsbrowser.OperationListHostedZonesByName:
		allowed["dns-name"] = true
	case awsbrowser.OperationListResourceRecordSets:
		for _, name := range []string{"hosted-zone-id", "record-name", "record-type", "record-identifier"} {
			allowed[name] = true
		}
	default:
		return nil, awsbrowser.ErrInvalidProviderOperation
	}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 || strings.TrimSpace(items[0]) == "" {
			return nil, awsbrowser.ErrInvalidQueryKey
		}
		params[name] = items[0]
	}
	if key.Operation == awsbrowser.OperationListHostedZonesByName {
		if name, ok := params["dns-name"]; ok {
			name, err = canonicalDNSName(name)
			if err != nil {
				return nil, err
			}
			params["dns-name"] = name
		}
	}
	if key.Operation == awsbrowser.OperationListResourceRecordSets {
		zoneID, ok := params["hosted-zone-id"]
		if !ok {
			return nil, awsbrowser.ErrInvalidQueryKey
		}
		zoneID, err = canonicalHostedZoneID(zoneID)
		if err != nil {
			return nil, err
		}
		params["hosted-zone-id"] = zoneID
		name, hasName := params["record-name"]
		recordType, hasType := params["record-type"]
		_, hasIdentifier := params["record-identifier"]
		if hasType && !hasName || hasIdentifier && !hasType {
			return nil, awsbrowser.ErrInvalidQueryKey
		}
		if hasName {
			name, err = canonicalDNSName(name)
			if err != nil {
				return nil, err
			}
			params["record-name"] = name
		}
		if hasType {
			normalized, ok := validRRType(recordType)
			if !ok {
				return nil, awsbrowser.ErrInvalidQueryKey
			}
			params["record-type"] = string(normalized)
		}
	}
	return params, nil
}

func (executor *route53Executor) listHostedZones(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	var marker *string
	seen := make(map[string]struct{})
	for pageNumber := uint64(0); ; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		output, err := executor.client.ListHostedZones(ctx, &route53.ListHostedZonesInput{Marker: marker, MaxItems: aws.Int32(zonePageSize)})
		if err != nil {
			return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderRoute53, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources, err := mapHostedZones(ctx, key, output.HostedZones, fetchedAt)
		if err != nil {
			return err
		}
		if err := emitPage(ctx, sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		if !output.IsTruncated {
			return complete(ctx, sink, executor.clock())
		}
		next, err := requiredCursor(output.NextMarker)
		if err != nil || marker != nil && next == *marker {
			return awsbrowser.ErrQueryDecode
		}
		if _, repeated := seen[next]; repeated {
			return awsbrowser.ErrQueryDecode
		}
		seen[next] = struct{}{}
		marker = aws.String(next)
	}
}

type zoneNameCursor struct {
	name string
	id   string
}

func (executor *route53Executor) listHostedZonesByName(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink, params map[string]string) error {
	var cursor zoneNameCursor
	if name := params["dns-name"]; name != "" {
		cursor.name = name
	}
	seen := make(map[zoneNameCursor]struct{})
	for pageNumber := uint64(0); ; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		input := &route53.ListHostedZonesByNameInput{MaxItems: aws.Int32(zonePageSize)}
		if cursor.name != "" {
			input.DNSName = aws.String(cursor.name)
		}
		if cursor.id != "" {
			input.HostedZoneId = aws.String(cursor.id)
		}
		output, err := executor.client.ListHostedZonesByName(ctx, input)
		if err != nil {
			return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderRoute53, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources, leftTarget, err := mapHostedZonesByName(ctx, key, output.HostedZones, fetchedAt, params["dns-name"])
		if err != nil {
			return err
		}
		if err := emitPage(ctx, sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		if !output.IsTruncated {
			return complete(ctx, sink, executor.clock())
		}
		nextName, err := requiredCursor(output.NextDNSName)
		if err != nil {
			return awsbrowser.ErrQueryDecode
		}
		nextName, err = canonicalDNSName(nextName)
		if err != nil {
			return awsbrowser.ErrQueryDecode
		}
		nextID, err := requiredCursor(output.NextHostedZoneId)
		if err != nil {
			return awsbrowser.ErrQueryDecode
		}
		nextID, err = canonicalHostedZoneID(nextID)
		if err != nil {
			return awsbrowser.ErrQueryDecode
		}
		next := zoneNameCursor{name: nextName, id: nextID}
		if next == cursor {
			return awsbrowser.ErrQueryDecode
		}
		if _, repeated := seen[next]; repeated {
			return awsbrowser.ErrQueryDecode
		}
		seen[next] = struct{}{}
		if params["dns-name"] != "" && (leftTarget || next.name != params["dns-name"]) {
			return complete(ctx, sink, executor.clock())
		}
		cursor = next
	}
}

func mapHostedZonesByName(ctx context.Context, key awsbrowser.QueryKey, zones []types.HostedZone, fetchedAt time.Time, targetName string) ([]awsbrowser.ObservedResource, bool, error) {
	if targetName == "" {
		resources, err := mapHostedZones(ctx, key, zones, fetchedAt)
		return resources, false, err
	}
	matching := make([]types.HostedZone, 0, len(zones))
	for _, zone := range zones {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		name, err := canonicalDNSName(aws.ToString(zone.Name))
		if err != nil {
			return nil, false, awsbrowser.ErrQueryDecode
		}
		if name != targetName {
			resources, mapErr := mapHostedZones(ctx, key, matching, fetchedAt)
			return resources, true, mapErr
		}
		matching = append(matching, zone)
	}
	resources, err := mapHostedZones(ctx, key, matching, fetchedAt)
	return resources, false, err
}

type recordCursor struct {
	name       string
	recordType types.RRType
	identifier string
	hasID      bool
}

func (executor *route53Executor) listResourceRecordSets(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink, params map[string]string) error {
	zoneID := params["hosted-zone-id"]
	targetName := params["record-name"]
	targetType, targetHasType := validRRType(params["record-type"])
	targetIdentifier, targetHasID := params["record-identifier"]
	cursor := recordCursor{name: targetName, recordType: targetType, identifier: targetIdentifier, hasID: targetHasID}
	seen := make(map[recordCursor]struct{})
	for pageNumber := uint64(0); ; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		input := &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(zoneID), MaxItems: aws.Int32(recordSetPageSize)}
		if cursor.name != "" {
			input.StartRecordName = aws.String(cursor.name)
		}
		if cursor.recordType != "" {
			input.StartRecordType = cursor.recordType
		}
		if cursor.hasID {
			input.StartRecordIdentifier = aws.String(cursor.identifier)
		}
		output, err := executor.client.ListResourceRecordSets(ctx, input)
		if err != nil {
			return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderRoute53, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources, leftTarget, err := mapRecordSets(ctx, key, zoneID, output.ResourceRecordSets, fetchedAt, targetName, targetType, targetHasType, targetIdentifier, targetHasID)
		if err != nil {
			return err
		}
		if err := emitPage(ctx, sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		if !output.IsTruncated {
			return complete(ctx, sink, executor.clock())
		}
		next, err := nextRecordCursor(output)
		if err != nil || next == cursor {
			return awsbrowser.ErrQueryDecode
		}
		if _, repeated := seen[next]; repeated {
			return awsbrowser.ErrQueryDecode
		}
		seen[next] = struct{}{}
		if targetName != "" && (leftTarget || next.name != targetName) {
			return complete(ctx, sink, executor.clock())
		}
		cursor = next
	}
}

func nextRecordCursor(output *route53.ListResourceRecordSetsOutput) (recordCursor, error) {
	name, err := requiredCursor(output.NextRecordName)
	if err != nil {
		return recordCursor{}, err
	}
	name, err = canonicalDNSName(name)
	if err != nil {
		return recordCursor{}, err
	}
	recordType, ok := validRRType(string(output.NextRecordType))
	if !ok {
		return recordCursor{}, awsbrowser.ErrQueryDecode
	}
	cursor := recordCursor{name: name, recordType: recordType}
	if output.NextRecordIdentifier != nil {
		cursor.identifier = strings.TrimSpace(*output.NextRecordIdentifier)
		if cursor.identifier == "" || hasControl(cursor.identifier) {
			return recordCursor{}, awsbrowser.ErrQueryDecode
		}
		cursor.hasID = true
	}
	return cursor, nil
}

func mapHostedZones(ctx context.Context, key awsbrowser.QueryKey, zones []types.HostedZone, fetchedAt time.Time) ([]awsbrowser.ObservedResource, error) {
	resources := make([]awsbrowser.ObservedResource, 0, len(zones))
	for _, zone := range zones {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, err := canonicalHostedZoneID(aws.ToString(zone.Id))
		if err != nil {
			return nil, awsbrowser.ErrQueryDecode
		}
		name, err := canonicalDNSName(aws.ToString(zone.Name))
		if err != nil {
			return nil, awsbrowser.ErrQueryDecode
		}
		private := zone.Config != nil && zone.Config.PrivateZone
		fields := map[string]any{"id": id, "name": name, "private": private}
		if zone.ResourceRecordSetCount != nil {
			fields["record_count"] = *zone.ResourceRecordSetCount
		}
		if zone.Config != nil && zone.Config.Comment != nil {
			fields["comment"] = *zone.Config.Comment
		}
		resourceKey, err := awsbrowser.NewGlobalResourceKey(key.Context, hostedZoneType, id)
		if err != nil {
			return nil, err
		}
		observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
		if err != nil {
			return nil, err
		}
		resources = append(resources, awsbrowser.ObservedResource{Key: resourceKey, Observation: observation})
	}
	return resources, nil
}

func mapRecordSets(ctx context.Context, key awsbrowser.QueryKey, zoneID string, sets []types.ResourceRecordSet, fetchedAt time.Time, targetName string, targetType types.RRType, targetHasType bool, targetIdentifier string, targetHasID bool) ([]awsbrowser.ObservedResource, bool, error) {
	resources := make([]awsbrowser.ObservedResource, 0, len(sets))
	leftTarget := false
	for _, set := range sets {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		name, err := canonicalDNSName(aws.ToString(set.Name))
		if err != nil {
			return nil, false, awsbrowser.ErrQueryDecode
		}
		recordType, ok := validRRType(string(set.Type))
		if !ok {
			return nil, false, awsbrowser.ErrQueryDecode
		}
		if targetName != "" && name != targetName {
			leftTarget = true
			break
		}
		identifier := aws.ToString(set.SetIdentifier)
		if targetHasType && recordType != targetType || targetHasID && identifier != targetIdentifier {
			continue
		}
		routingIdentity, routingFields, err := recordRouting(set)
		if err != nil {
			return nil, false, err
		}
		identity := url.Values{
			"name":    []string{name},
			"routing": []string{routingIdentity},
			"type":    []string{string(recordType)},
			"zone":    []string{zoneID},
		}.Encode()
		resourceKey, err := awsbrowser.NewGlobalResourceKey(key.Context, recordSetType, identity)
		if err != nil {
			return nil, false, err
		}
		zoneKey, err := awsbrowser.NewGlobalResourceKey(key.Context, hostedZoneType, zoneID)
		if err != nil {
			return nil, false, err
		}
		zoneRelation, err := relationFields(awsbrowser.RelationAPIExact, "record-listed-from-hosted-zone", key.Operation, fetchedAt)
		if err != nil {
			return nil, false, err
		}
		zoneRelation["source"] = resourceKey
		zoneRelation["target"] = zoneKey
		fields := map[string]any{
			"hosted_zone_id":  zoneID,
			"hosted_zone_key": zoneKey,
			"name":            name,
			"type":            string(recordType),
			"routing":         routingFields,
			"zone_relation":   zoneRelation,
		}
		if set.SetIdentifier != nil {
			fields["set_identifier"] = *set.SetIdentifier
		}
		if set.TTL != nil {
			fields["ttl"] = *set.TTL
		}
		if set.HealthCheckId != nil {
			fields["health_check_id"] = *set.HealthCheckId
		}
		if set.TrafficPolicyInstanceId != nil {
			fields["traffic_policy_instance_id"] = *set.TrafficPolicyInstanceId
		}
		if len(set.ResourceRecords) != 0 {
			values := make([]string, 0, len(set.ResourceRecords))
			for _, record := range set.ResourceRecords {
				if record.Value == nil {
					return nil, false, awsbrowser.ErrQueryDecode
				}
				values = append(values, *record.Value)
			}
			fields["values"] = values
		}
		if set.AliasTarget != nil {
			aliasZoneID, err := canonicalHostedZoneID(aws.ToString(set.AliasTarget.HostedZoneId))
			if err != nil {
				return nil, false, awsbrowser.ErrQueryDecode
			}
			aliasName, err := canonicalDNSName(aws.ToString(set.AliasTarget.DNSName))
			if err != nil {
				return nil, false, awsbrowser.ErrQueryDecode
			}
			fields["alias"] = map[string]any{
				"dns_name":               aliasName,
				"hosted_zone_id":         aliasZoneID,
				"evaluate_target_health": set.AliasTarget.EvaluateTargetHealth,
			}
			aliasRelation, err := relationFields(awsbrowser.RelationAPIExact, "alias-target-returned-by-api", key.Operation, fetchedAt)
			if err != nil {
				return nil, false, err
			}
			aliasRelation["source"] = resourceKey
			if key.Context.Partition == "aws" && aliasZoneID == cloudFrontZoneID && strings.HasSuffix(aliasName, ".cloudfront.net.") {
				target, err := awsbrowser.NewGlobalResourceKey(key.Context, "cloudfront.distribution-domain", strings.TrimSuffix(aliasName, "."))
				if err != nil {
					return nil, false, err
				}
				aliasRelation["target"] = target
			}
			fields["alias_relation"] = aliasRelation
		}
		observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
		if err != nil {
			return nil, false, err
		}
		resources = append(resources, awsbrowser.ObservedResource{Key: resourceKey, Observation: observation})
	}
	return resources, leftTarget, nil
}

func relationFields(kind awsbrowser.RelationKind, reason, operation string, observedAt time.Time) (map[string]any, error) {
	evidence, err := awsbrowser.NewRelationEvidence(kind, reason, operation, awsbrowser.GlobalRegion, observedAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"kind":        string(evidence.Kind),
		"reason":      evidence.Reason,
		"scope":       evidence.Scope,
		"operation":   evidence.Operation,
		"observed_at": evidence.ObservedAt,
	}, nil
}

func recordRouting(set types.ResourceRecordSet) (string, map[string]any, error) {
	fields := map[string]any{}
	putString := func(name, value string) {
		if value != "" {
			fields[name] = value
		}
	}
	putString("set_identifier", aws.ToString(set.SetIdentifier))
	putString("failover", string(set.Failover))
	putString("region", string(set.Region))
	if set.Weight != nil {
		fields["weight"] = *set.Weight
	}
	if set.MultiValueAnswer != nil {
		fields["multi_value"] = *set.MultiValueAnswer
	}
	if geo := set.GeoLocation; geo != nil {
		putString("geo_continent", aws.ToString(geo.ContinentCode))
		putString("geo_country", aws.ToString(geo.CountryCode))
		putString("geo_subdivision", aws.ToString(geo.SubdivisionCode))
	}
	if cidr := set.CidrRoutingConfig; cidr != nil {
		putString("cidr_collection", aws.ToString(cidr.CollectionId))
		putString("cidr_location", aws.ToString(cidr.LocationName))
	}
	if geo := set.GeoProximityLocation; geo != nil {
		putString("geoproximity_region", aws.ToString(geo.AWSRegion))
		putString("geoproximity_local_zone_group", aws.ToString(geo.LocalZoneGroup))
		if geo.Bias != nil {
			fields["geoproximity_bias"] = *geo.Bias
		}
		if geo.Coordinates != nil {
			putString("geoproximity_latitude", aws.ToString(geo.Coordinates.Latitude))
			putString("geoproximity_longitude", aws.ToString(geo.Coordinates.Longitude))
		}
	}
	policy, err := stableRoutingPolicy(set)
	if err != nil {
		return "", nil, err
	}
	fields["policy"] = policy
	identity := url.Values{"policy": []string{policy}}
	if set.SetIdentifier != nil {
		identifier := *set.SetIdentifier
		if strings.TrimSpace(identifier) == "" || hasControl(identifier) {
			return "", nil, awsbrowser.ErrQueryDecode
		}
		identity.Set("set_identifier", identifier)
	} else if policy != "simple" {
		// Route 53 requires SetIdentifier for non-simple routing. Without it,
		// two variants would collapse to the same canonical identity.
		return "", nil, awsbrowser.ErrQueryDecode
	}
	return identity.Encode(), fields, nil
}

func stableRoutingPolicy(set types.ResourceRecordSet) (string, error) {
	policies := make([]string, 0, 1)
	if set.Weight != nil {
		policies = append(policies, "weighted")
	}
	if set.Failover != "" {
		policies = append(policies, "failover")
	}
	if set.Region != "" {
		policies = append(policies, "latency")
	}
	if set.MultiValueAnswer != nil && *set.MultiValueAnswer {
		policies = append(policies, "multivalue")
	}
	if set.GeoLocation != nil {
		policies = append(policies, "geolocation")
	}
	if set.CidrRoutingConfig != nil {
		policies = append(policies, "cidr")
	}
	if set.GeoProximityLocation != nil {
		policies = append(policies, "geoproximity")
	}
	if len(policies) > 1 {
		return "", awsbrowser.ErrQueryDecode
	}
	if len(policies) == 0 {
		return "simple", nil
	}
	return policies[0], nil
}

func emitPage(ctx context.Context, sink awsbrowser.QueryPageSink, number uint64, resources []awsbrowser.ObservedResource, fetchedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	page, err := awsbrowser.NewQueryPage(number, resources, fetchedAt, true)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return sink.Page(page)
}

func complete(ctx context.Context, sink awsbrowser.QueryPageSink, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return sink.Complete(at.UTC())
}

func requiredCursor(value *string) (string, error) {
	if value == nil {
		return "", awsbrowser.ErrQueryDecode
	}
	result := strings.TrimSpace(*value)
	if result == "" || hasControl(result) {
		return "", awsbrowser.ErrQueryDecode
	}
	return result, nil
}

func canonicalHostedZoneID(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "/hostedzone/")
	if len(value) < 2 || len(value) > 64 || value[0] != 'Z' {
		return "", awsbrowser.ErrInvalidQueryKey
	}
	for _, character := range value {
		if !asciiLetterOrDigit(character) {
			return "", awsbrowser.ErrInvalidQueryKey
		}
	}
	return value, nil
}

func canonicalDNSName(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || hasControl(value) || strings.ContainsAny(value, " \t\r\n") {
		return "", awsbrowser.ErrInvalidQueryKey
	}
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
	}
	if value == "" || strings.HasSuffix(value, ".") || len(value) > 253 {
		return "", awsbrowser.ErrInvalidQueryKey
	}
	labels := strings.Split(value, ".")
	for index, label := range labels {
		if !validDNSLabel(label, index == 0) {
			return "", awsbrowser.ErrInvalidQueryKey
		}
	}
	return value + ".", nil
}

func validDNSLabel(label string, first bool) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if label == "*" {
		return first
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for index := 0; index < len(label); index++ {
		character := label[index]
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '-', character == '_':
		case character == '\\' && index+3 < len(label) && asciiDigit(label[index+1]) && asciiDigit(label[index+2]) && asciiDigit(label[index+3]):
			index += 3
		default:
			return false
		}
	}
	return true
}

func validRRType(value string) (types.RRType, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	candidate := types.RRType(value)
	for _, allowed := range candidate.Values() {
		if candidate == allowed {
			return candidate, true
		}
	}
	return "", false
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func asciiLetterOrDigit(character rune) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func asciiDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
