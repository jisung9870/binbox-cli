package bb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	awsintegration "github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/integration"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/snapshot"
)

const awsSnapshotRetention = 2
const awsSnapshotScopeTimeout = 3 * time.Minute

var awsSnapshotUnobservedServices = []string{"elbv2", "rds", "lambda", "ecs", "vpc-endpoint"}

type productionAWSSnapshotSyncService struct {
	core        awsSnapshotQueryCore
	coreFactory func() (awsSnapshotQueryCore, error)
	groups      []awsbrowser.ContextGroup
	path        string
	now         func() time.Time
}

type productionAWSSnapshotReadService struct {
	path string
}

type awsSGSnapshotCollector struct {
	core awsSnapshotQueryCore
}

type awsSnapshotCollector struct {
	core awsSnapshotQueryCore
}

func awsSnapshotPath(stateRoot string) string {
	return filepath.Join(stateRoot, "aws", "snapshot.db")
}

func (a *App) localSnapshotReadService() (awsSnapshotReadService, error) {
	_, stateRoot, err := a.paths()
	if err != nil {
		return nil, err
	}
	return &productionAWSSnapshotReadService{path: awsSnapshotPath(stateRoot)}, nil
}

func (service *productionAWSSnapshotSyncService) Sync(ctx context.Context, request awsSnapshotSyncRequest) (snapshot.Run, []snapshot.Coverage, error) {
	if service == nil || service.now == nil || ctx == nil {
		return snapshot.Run{}, nil, errors.New("invalid snapshot sync service")
	}
	var selected *awsbrowser.ContextGroup
	for index := range service.groups {
		if service.groups[index].Name == request.Group {
			selected = &service.groups[index]
			break
		}
	}
	if selected == nil {
		return snapshot.Run{}, nil, invalid("AWS context group not found: " + request.Group)
	}
	core := service.core
	if core == nil && service.coreFactory != nil {
		var err error
		core, err = service.coreFactory()
		if err != nil {
			return snapshot.Run{}, nil, err
		}
	}
	if core == nil {
		return snapshot.Run{}, nil, errors.New("invalid snapshot sync service")
	}
	services := []string{"ec2-sg"}
	switch request.Collection {
	case "", "sg":
	case "graph":
		services = append(services, "ec2-vpc-peering")
	default:
		return snapshot.Run{}, nil, invalid("unsupported AWS snapshot collection: " + request.Collection)
	}
	scopes := make([]snapshot.Scope, 0, len(selected.Profiles)*len(selected.Regions)*len(services))
	for _, profile := range selected.Profiles {
		for _, region := range selected.Regions {
			for _, serviceName := range services {
				scopes = append(scopes, snapshot.Scope{Profile: profile, Region: region, Service: serviceName})
			}
			if request.Collection == "graph" {
				for _, serviceName := range []string{"ec2-transit-gateway", "ec2-privatelink"} {
					scopes = append(scopes, snapshot.Scope{
						Profile: profile, Region: region, Service: serviceName,
						NotObserved: true, NotObservedReason: "collector-not-implemented",
					})
				}
			}
		}
	}
	var input snapshot.RunInput
	var run snapshot.Run
	err := withFileLockContext(ctx, service.path+".sync", func() error {
		var err error
		input, err = (snapshot.Coordinator{
			Collector: &awsSnapshotCollector{core: core}, Now: service.now, Concurrency: awsRuntimeConcurrency,
		}).Collect(ctx, scopes)
		if err != nil {
			return err
		}
		return withFileLockContext(ctx, service.path, func() error {
			store, _, err := snapshot.Open(ctx, service.path, awsSnapshotRetention)
			if err != nil {
				return err
			}
			defer store.Close()
			run, err = store.CommitRun(ctx, input)
			return err
		})
	})
	return run, input.Coverage, err
}

func (service *productionAWSSnapshotReadService) Refs(ctx context.Context, request awsSnapshotRefsRequest) (awsSnapshotRefsExecution, error) {
	if service == nil || ctx == nil {
		return awsSnapshotRefsExecution{}, errors.New("invalid snapshot read service")
	}
	if _, err := os.Lstat(service.path); errors.Is(err, os.ErrNotExist) {
		return awsSnapshotRefsExecution{}, unavailable("AWS snapshot not found; run 'bb aws sync sg --group <configured-context-group>'")
	} else if err != nil {
		return awsSnapshotRefsExecution{}, err
	}
	var execution awsSnapshotRefsExecution
	err := withFileLockContext(ctx, service.path, func() error {
		store, err := snapshot.OpenReadOnly(ctx, service.path)
		if err != nil {
			return err
		}
		defer store.Close()
		view, err := store.ActiveView(ctx)
		if err != nil {
			return err
		}
		defer view.Close()
		resourceType := "ec2.security-group"
		if request.Kind == "vpc" {
			resourceType = "ec2.vpc"
		}
		target := snapshot.ResourceRef{
			Partition: request.Partition, AccountID: request.AccountID, Region: request.Region,
			Type: resourceType, ID: request.ResourceID,
		}
		observed, err := view.ResourceObserved(ctx, target)
		if err != nil {
			return err
		}
		edges, err := view.Reverse(ctx, target, awsSnapshotRefsLimit+1)
		if err != nil {
			return err
		}
		coverage, err := view.Coverage(ctx)
		if err != nil {
			return err
		}
		truncated := len(edges) > awsSnapshotRefsLimit
		if truncated {
			edges = edges[:awsSnapshotRefsLimit]
		}
		execution = awsSnapshotRefsExecution{
			Run: view.Run(), Target: target, ResourceObserved: observed, Edges: edges, Coverage: coverage,
			Truncated: truncated,
		}
		return nil
	})
	return execution, err
}

func (collector *awsSnapshotCollector) Collect(ctx context.Context, scope snapshot.Scope) (snapshot.Collection, error) {
	if collector == nil || collector.core == nil {
		return snapshot.Collection{}, &snapshot.CollectionError{Kind: "unsupported", Err: errors.New("snapshot collector unavailable")}
	}
	switch scope.Service {
	case "ec2-sg":
		return (&awsSGSnapshotCollector{core: collector.core}).Collect(ctx, scope)
	case "ec2-vpc-peering":
		return collector.collectVpcPeering(ctx, scope)
	default:
		return snapshot.Collection{}, &snapshot.CollectionError{Kind: "unsupported", Err: errors.New("snapshot collector unavailable")}
	}
}

func (collector *awsSnapshotCollector) collectVpcPeering(ctx context.Context, scope snapshot.Scope) (snapshot.Collection, error) {
	ctx, cancel := context.WithTimeout(ctx, awsSnapshotScopeTimeout)
	defer cancel()
	result, err := collector.core.Query(ctx, awsintegration.Request{
		Profile: scope.Profile, Region: scope.Region, Provider: awsbrowser.ProviderEC2,
		Operation: awsbrowser.OperationDescribeVpcPeeringConnections,
	})
	if err != nil {
		kind := "unknown"
		if result.Update.Failure != nil {
			kind = string(result.Update.Failure.Kind)
		} else if errors.Is(err, context.DeadlineExceeded) {
			kind = string(awsbrowser.ProviderTimedOut)
		}
		return snapshot.Collection{}, &snapshot.CollectionError{Kind: kind, Err: errors.New("AWS VPC peering collection failed")}
	}
	if result.Update.Key == nil || result.Update.Key.Validate() != nil || result.Update.Key.Context.Profile != scope.Profile || result.Update.Key.Context.Region != scope.Region {
		return snapshot.Collection{}, &snapshot.CollectionError{Kind: "context-changed", Err: errors.New("AWS snapshot context changed")}
	}
	collection := snapshot.Collection{AccountID: result.Update.Key.Context.AccountID}
	remoteCoverage := map[string]snapshot.Coverage{}
	for _, page := range result.Update.Snapshot.Pages() {
		for _, observed := range page.Resources() {
			resourceRef, refErr := snapshot.RefFromKey(observed.Key)
			if refErr != nil {
				return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid VPC peering resource")}
			}
			fields := observed.Observation.Fields()
			collection.Resources = append(collection.Resources, snapshot.Resource{Ref: resourceRef, Name: awsSnapshotResourceName(fields)})
			relations, relationErr := awsbrowser.RelationsFromMappedFields(fields)
			if relationErr != nil {
				return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid VPC peering relation")}
			}
			for _, relation := range relations {
				if relation.Source.Type != "ec2.vpc-peering-connection" || relation.Target.Type != "ec2.vpc" || relation.Semantics.Type != awsbrowser.RelationAssociatedWith {
					continue
				}
				converted, convertErr := snapshot.RelationsFromBrowser(relation, scope.Profile)
				if convertErr != nil {
					return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid VPC peering evidence")}
				}
				collection.Relations = append(collection.Relations, converted...)
				if relation.Target.AccountID != collection.AccountID {
					key := relation.Target.AccountID + "\x00" + relation.Target.Region
					remoteCoverage[key] = snapshot.Coverage{
						Profile: scope.Profile, AccountID: relation.Target.AccountID, Region: relation.Target.Region,
						Service: "ec2-vpc-peering-participant", Status: snapshot.CoverageNotObserved,
						ErrorKind: "participant-account-not-searched",
					}
				}
			}
		}
	}
	remoteKeys := make([]string, 0, len(remoteCoverage))
	for key := range remoteCoverage {
		remoteKeys = append(remoteKeys, key)
	}
	sort.Strings(remoteKeys)
	for _, key := range remoteKeys {
		collection.Coverage = append(collection.Coverage, remoteCoverage[key])
	}
	return collection, nil
}

func (collector *awsSGSnapshotCollector) Collect(ctx context.Context, scope snapshot.Scope) (snapshot.Collection, error) {
	if collector == nil || collector.core == nil || ctx == nil {
		return snapshot.Collection{}, &snapshot.CollectionError{Kind: "unsupported", Err: errors.New("snapshot collector unavailable")}
	}
	ctx, cancel := context.WithTimeout(ctx, awsSnapshotScopeTimeout)
	defer cancel()
	collection := snapshot.Collection{}
	operations := []string{
		awsbrowser.OperationDescribeSecurityGroups,
		awsbrowser.OperationDescribeSecurityGroupRules,
		awsbrowser.OperationDescribeInstances,
	}
	for _, operation := range operations {
		result, err := collector.core.Query(ctx, awsintegration.Request{
			Profile: scope.Profile, Region: scope.Region, Provider: awsbrowser.ProviderEC2, Operation: operation,
		})
		if err != nil {
			kind := "unknown"
			if result.Update.Failure != nil {
				kind = string(result.Update.Failure.Kind)
			} else if errors.Is(err, context.DeadlineExceeded) {
				kind = string(awsbrowser.ProviderTimedOut)
			}
			return collection, &snapshot.CollectionError{Kind: kind, Err: errors.New("AWS snapshot collection failed")}
		}
		if result.Update.Key == nil || result.Update.Key.Validate() != nil ||
			result.Update.Key.Context.Profile != scope.Profile || result.Update.Key.Context.Region != scope.Region {
			return collection, &snapshot.CollectionError{Kind: "context-changed", Err: errors.New("AWS snapshot context changed")}
		}
		accountID := result.Update.Key.Context.AccountID
		if collection.AccountID == "" {
			collection.AccountID = accountID
		} else if collection.AccountID != accountID {
			return collection, &snapshot.CollectionError{Kind: "context-changed", Err: errors.New("AWS snapshot account changed")}
		}
		for _, page := range result.Update.Snapshot.Pages() {
			for _, observed := range page.Resources() {
				resourceRef, err := snapshot.RefFromKey(observed.Key)
				if err != nil {
					return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid AWS snapshot resource")}
				}
				fields := observed.Observation.Fields()
				collection.Resources = append(collection.Resources, snapshot.Resource{Ref: resourceRef, Name: awsSnapshotResourceName(fields)})
				relations, err := awsbrowser.RelationsFromMappedFields(fields)
				if err != nil {
					return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid AWS snapshot relation")}
				}
				for _, relation := range relations {
					if !awsSnapshotSGRelation(operation, relation) {
						continue
					}
					converted, err := snapshot.RelationsFromBrowser(relation, scope.Profile)
					if err != nil {
						return collection, &snapshot.CollectionError{Kind: "decode", Err: errors.New("invalid AWS snapshot evidence")}
					}
					collection.Relations = append(collection.Relations, converted...)
				}
			}
		}
	}
	for _, service := range awsSnapshotUnobservedServices {
		collection.Coverage = append(collection.Coverage, snapshot.Coverage{
			Profile: scope.Profile, AccountID: collection.AccountID, Region: scope.Region, Service: service,
			Status: snapshot.CoverageNotObserved, ErrorKind: "ec2-only",
		})
	}
	return collection, nil
}

func awsSnapshotSGRelation(operation string, relation awsbrowser.Relation) bool {
	switch operation {
	case awsbrowser.OperationDescribeSecurityGroupRules:
		return relation.Semantics.Type == awsbrowser.RelationReferences &&
			relation.Source.Type == "ec2.security-group" && relation.Target.Type == "ec2.security-group"
	case awsbrowser.OperationDescribeInstances:
		return relation.Semantics.Type == awsbrowser.RelationUses && relation.Target.Type == "ec2.security-group"
	default:
		return false
	}
}

func awsSnapshotResourceName(fields map[string]any) string {
	switch tags := fields["tags"].(type) {
	case map[string]string:
		return strings.TrimSpace(tags["Name"])
	case map[string]any:
		if value, ok := tags["Name"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if value, ok := fields["name"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
