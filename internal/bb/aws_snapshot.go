package bb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/snapshot"
)

var (
	awsSnapshotAccountRE   = regexp.MustCompile(`^[0-9]{12}$`)
	awsSnapshotPartitionRE = regexp.MustCompile(`^aws(?:-[a-z0-9-]+)?$`)
	awsSnapshotSGRE        = regexp.MustCompile(`^sg-[0-9a-f]+$`)
)

const awsSnapshotRefsLimit = 10_000

type awsSnapshotSyncRequest struct {
	Group string `json:"group"`
}

type awsSnapshotRefsRequest struct {
	Partition string `json:"partition"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	GroupID   string `json:"group_id"`
}

type awsSnapshotSyncService interface {
	Sync(context.Context, awsSnapshotSyncRequest) (snapshot.Run, []snapshot.Coverage, error)
}

type awsSnapshotReadService interface {
	Refs(context.Context, awsSnapshotRefsRequest) (awsSnapshotRefsExecution, error)
}

type awsSnapshotSyncServiceFactory func() (awsSnapshotSyncService, error)
type awsSnapshotReadServiceFactory func() (awsSnapshotReadService, error)

type awsSnapshotRefsExecution struct {
	Run              snapshot.Run
	Target           snapshot.ResourceRef
	ResourceObserved bool
	Edges            []snapshot.Edge
	Coverage         []snapshot.Coverage
	Truncated        bool
}

type awsSnapshotRunData struct {
	ID            string    `json:"id"`
	CompletedAt   time.Time `json:"completed_at"`
	AgeSeconds    int64     `json:"age_seconds"`
	SchemaVersion int       `json:"schema_version"`
}

type awsSnapshotCoverageScope struct {
	Profile   string `json:"profile"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	Service   string `json:"service"`
	Status    string `json:"status"`
	ErrorKind string `json:"error_kind"`
}

type awsSnapshotCoverageData struct {
	Total                  int                        `json:"total"`
	Succeeded              int                        `json:"succeeded"`
	Failed                 int                        `json:"failed"`
	NotObserved            int                        `json:"not_observed"`
	RuleReferencesComplete bool                       `json:"rule_references_complete"`
	AttachmentsComplete    bool                       `json:"attachments_complete"`
	Complete               bool                       `json:"complete"`
	Scopes                 []awsSnapshotCoverageScope `json:"scopes"`
}

type awsSnapshotResourceData struct {
	Partition string `json:"partition"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

type awsSnapshotObserverData struct {
	Profile    string    `json:"profile"`
	AccountID  string    `json:"account_id"`
	Region     string    `json:"region"`
	ObservedAt time.Time `json:"observed_at"`
}

type awsSnapshotEdgeData struct {
	Source       awsSnapshotResourceData   `json:"source"`
	Target       awsSnapshotResourceData   `json:"target"`
	RelationType string                    `json:"relation_type"`
	Direction    string                    `json:"direction"`
	Confidence   string                    `json:"confidence"`
	Condition    string                    `json:"condition"`
	Reason       string                    `json:"reason"`
	Operation    string                    `json:"operation"`
	Scope        string                    `json:"scope"`
	ObservedAt   time.Time                 `json:"observed_at"`
	Observers    []awsSnapshotObserverData `json:"observers"`
}

type awsSnapshotSyncData struct {
	Source   string                  `json:"source"`
	Group    string                  `json:"group"`
	Run      awsSnapshotRunData      `json:"run"`
	Coverage awsSnapshotCoverageData `json:"coverage"`
}

type awsSnapshotRefsData struct {
	Source           string                  `json:"source"`
	Run              awsSnapshotRunData      `json:"run"`
	Target           awsSnapshotResourceData `json:"target"`
	ResourceObserved bool                    `json:"resource_observed"`
	References       []awsSnapshotEdgeData   `json:"references"`
	Limit            int                     `json:"limit"`
	Truncated        bool                    `json:"truncated"`
	Coverage         awsSnapshotCoverageData `json:"coverage"`
}

func (a *App) awsSnapshotSyncCommand(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprint(a.out, "Usage: bb aws sync sg --group <configured-context-group> [--json]\n")
		return err
	}
	request, jsonMode, err := parseAWSSnapshotSync(args)
	if err != nil {
		return err
	}
	if a.awsSnapshotSync == nil {
		return unavailable("AWS snapshot sync is not available in this build")
	}
	service, err := a.awsSnapshotSync()
	if err != nil {
		return mapAWSSnapshotFailure(err, "initialize AWS snapshot sync")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run, coverage, err := service.Sync(ctx, request)
	if err != nil {
		return mapAWSSnapshotFailure(err, "sync AWS security-group snapshot")
	}
	data := awsSnapshotSyncData{Source: "snapshot", Group: request.Group, Run: awsSnapshotRun(run, a.now()), Coverage: awsSnapshotCoverage(coverage)}
	warnings := awsSnapshotWarnings(data.Coverage, false)
	if jsonMode {
		return printEnvelope(a.out, data, warnings)
	}
	return renderAWSSnapshotSync(a.out, data, warnings)
}

func (a *App) awsSnapshotRefsCommand(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprint(a.out, "Usage: bb aws refs sg <sg-id> --account <12-digit-id> --region <region> [--partition <partition>] [--json]\n")
		return err
	}
	request, jsonMode, err := parseAWSSnapshotRefs(args)
	if err != nil {
		return err
	}
	if a.awsSnapshotRead == nil {
		return unavailable("AWS snapshot reader is not available in this build")
	}
	service, err := a.awsSnapshotRead()
	if err != nil {
		return mapAWSSnapshotFailure(err, "initialize AWS snapshot reader")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	execution, err := service.Refs(ctx, request)
	if err != nil {
		return mapAWSSnapshotFailure(err, "read AWS security-group references")
	}
	data, err := normalizeAWSSnapshotRefs(execution, a.now())
	if err != nil {
		return mapAWSSnapshotFailure(err, "decode AWS snapshot references")
	}
	warnings := awsSnapshotWarnings(data.Coverage, data.Truncated)
	if jsonMode {
		return printEnvelope(a.out, data, warnings)
	}
	return renderAWSSnapshotRefs(a.out, data, warnings)
}

func parseAWSSnapshotSync(args []string) (awsSnapshotSyncRequest, bool, error) {
	request := awsSnapshotSyncRequest{}
	if len(args) == 0 || args[0] != "sg" {
		return request, false, usage("aws sync", "sg --group <configured-context-group> [--json]")
	}
	jsonMode, groupSet := false, false
	for tail := args[1:]; len(tail) > 0; {
		switch tail[0] {
		case "--json":
			if jsonMode {
				return request, false, invalid("--json may be specified only once")
			}
			jsonMode, tail = true, tail[1:]
		case "--group":
			if groupSet || len(tail) < 2 || !validAWSSnapshotGroup(tail[1]) {
				return request, jsonMode, invalid("missing, invalid, or duplicate AWS context group")
			}
			request.Group, groupSet, tail = tail[1], true, tail[2:]
		default:
			return request, jsonMode, invalid(fmt.Sprintf("unknown aws sync argument %q", tail[0]))
		}
	}
	if !groupSet {
		return request, jsonMode, invalid("--group is required for explicit AWS snapshot sync")
	}
	return request, jsonMode, nil
}

func parseAWSSnapshotRefs(args []string) (awsSnapshotRefsRequest, bool, error) {
	request := awsSnapshotRefsRequest{Partition: "aws"}
	if len(args) < 2 || args[0] != "sg" || !awsSnapshotSGRE.MatchString(args[1]) {
		return request, false, usage("aws refs", "sg <sg-id> --account <12-digit-id> --region <region> [--partition <partition>] [--json]")
	}
	request.GroupID = args[1]
	jsonMode := false
	set := map[string]bool{}
	for tail := args[2:]; len(tail) > 0; {
		flag := tail[0]
		if flag == "--json" {
			if jsonMode {
				return request, false, invalid("--json may be specified only once")
			}
			jsonMode, tail = true, tail[1:]
			continue
		}
		if flag != "--account" && flag != "--region" && flag != "--partition" {
			return request, jsonMode, invalid(fmt.Sprintf("unknown aws refs argument %q", flag))
		}
		if set[flag] || len(tail) < 2 {
			return request, jsonMode, invalid("missing or duplicate value for " + flag)
		}
		value := tail[1]
		switch flag {
		case "--account":
			if !awsSnapshotAccountRE.MatchString(value) {
				return request, jsonMode, invalid("invalid AWS account ID")
			}
			request.AccountID = value
		case "--region":
			if awsbrowser.ValidateContextSelection("", value) != nil || value == "" {
				return request, jsonMode, invalid("invalid AWS region")
			}
			request.Region = value
		case "--partition":
			if !awsSnapshotPartitionRE.MatchString(value) {
				return request, jsonMode, invalid("invalid AWS partition")
			}
			request.Partition = value
		}
		set[flag], tail = true, tail[2:]
	}
	if request.AccountID == "" || request.Region == "" {
		return request, jsonMode, invalid("--account and --region are required for exact AWS snapshot lookup")
	}
	return request, jsonMode, nil
}

func validAWSSnapshotGroup(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= 64 && !strings.HasPrefix(value, "-") && safeAWSQueryText(value) == value
}

func awsSnapshotRun(run snapshot.Run, now time.Time) awsSnapshotRunData {
	age := now.UTC().Sub(run.CompletedAt.UTC())
	if age < 0 {
		age = 0
	}
	return awsSnapshotRunData{ID: run.ID, CompletedAt: run.CompletedAt.UTC(), AgeSeconds: int64(age / time.Second), SchemaVersion: run.SchemaVersion}
}

func awsSnapshotCoverage(values []snapshot.Coverage) awsSnapshotCoverageData {
	data := awsSnapshotCoverageData{Scopes: make([]awsSnapshotCoverageScope, 0, len(values))}
	ruleScopes, succeededRuleScopes := 0, 0
	for _, value := range values {
		data.Total++
		switch value.Status {
		case snapshot.CoverageSucceeded:
			data.Succeeded++
		case snapshot.CoverageFailed:
			data.Failed++
		case snapshot.CoverageNotObserved:
			data.NotObserved++
		}
		if value.Service == "ec2-sg" {
			ruleScopes++
			if value.Status == snapshot.CoverageSucceeded {
				succeededRuleScopes++
			}
		}
		data.Scopes = append(data.Scopes, awsSnapshotCoverageScope{
			Profile: value.Profile, AccountID: value.AccountID, Region: value.Region, Service: value.Service,
			Status: string(value.Status), ErrorKind: value.ErrorKind,
		})
	}
	data.RuleReferencesComplete = ruleScopes > 0 && succeededRuleScopes == ruleScopes
	data.AttachmentsComplete = data.RuleReferencesComplete && data.Failed == 0 && data.NotObserved == 0
	data.Complete = data.RuleReferencesComplete && data.AttachmentsComplete
	return data
}

func normalizeAWSSnapshotRefs(execution awsSnapshotRefsExecution, now time.Time) (awsSnapshotRefsData, error) {
	data := awsSnapshotRefsData{
		Source: "snapshot", Run: awsSnapshotRun(execution.Run, now), ResourceObserved: execution.ResourceObserved,
		Target: awsSnapshotResource(execution.Target, ""), Coverage: awsSnapshotCoverage(execution.Coverage),
		References: make([]awsSnapshotEdgeData, 0, len(execution.Edges)), Limit: awsSnapshotRefsLimit, Truncated: execution.Truncated,
	}
	for _, edge := range execution.Edges {
		if edge.Relation.Type != awsbrowser.RelationReferences && edge.Relation.Type != awsbrowser.RelationUses {
			continue
		}
		source, err := snapshot.ParseResourceRefKey(edge.SourceKey)
		if err != nil {
			return awsSnapshotRefsData{}, err
		}
		target, err := snapshot.ParseResourceRefKey(edge.TargetKey)
		if err != nil {
			return awsSnapshotRefsData{}, err
		}
		item := awsSnapshotEdgeData{
			Source: awsSnapshotResource(source, edge.SourceName), Target: awsSnapshotResource(target, edge.TargetName),
			RelationType: string(edge.Relation.Type), Direction: string(edge.Relation.Direction), Confidence: string(edge.Relation.Confidence),
			Condition: edge.Relation.Condition, Reason: edge.Relation.Reason, Operation: edge.Relation.Operation,
			Scope: edge.Relation.Scope, ObservedAt: edge.Relation.ObservedAt.UTC(), Observers: make([]awsSnapshotObserverData, 0, len(edge.Observers)),
		}
		for _, observer := range edge.Observers {
			item.Observers = append(item.Observers, awsSnapshotObserverData{Profile: observer.Profile, AccountID: observer.AccountID, Region: observer.Region, ObservedAt: observer.ObservedAt.UTC()})
		}
		data.References = append(data.References, item)
	}
	return data, nil
}

func awsSnapshotResource(ref snapshot.ResourceRef, name string) awsSnapshotResourceData {
	return awsSnapshotResourceData{Partition: ref.Partition, AccountID: ref.AccountID, Region: ref.Region, Type: ref.Type, ID: ref.ID, Name: name}
}

func awsSnapshotWarnings(coverage awsSnapshotCoverageData, truncated bool) []string {
	warnings := make([]string, 0, 2)
	if !coverage.Complete {
		warnings = append(warnings, "snapshot coverage is incomplete; failed or not-observed scopes are listed")
	}
	if truncated {
		warnings = append(warnings, "reference results were truncated at 10000 rows")
	}
	return warnings
}

func renderAWSSnapshotSync(out io.Writer, data awsSnapshotSyncData, warnings []string) error {
	if _, err := fmt.Fprintf(out, "AWS snapshot synced: group=%s run=%s\n", safeAWSQueryText(data.Group), safeAWSQueryText(data.Run.ID)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Coverage: %d succeeded, %d failed, %d not observed\n", data.Coverage.Succeeded, data.Coverage.Failed, data.Coverage.NotObserved); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Completeness: rule-references=%t attachments=%t\n", data.Coverage.RuleReferencesComplete, data.Coverage.AttachmentsComplete); err != nil {
		return err
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintln(out, "Warning:", safeAWSQueryText(warning)); err != nil {
			return err
		}
	}
	return nil
}

func renderAWSSnapshotRefs(out io.Writer, data awsSnapshotRefsData, warnings []string) error {
	if _, err := fmt.Fprintf(out, "AWS snapshot refs: %s account=%s region=%s\n", safeAWSQueryText(data.Target.ID), safeAWSQueryText(data.Target.AccountID), safeAWSQueryText(data.Target.Region)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Run: %s completed=%s age=%ds\n", safeAWSQueryText(data.Run.ID), data.Run.CompletedAt.Format(time.RFC3339), data.Run.AgeSeconds); err != nil {
		return err
	}
	if !data.ResourceObserved {
		if _, err := fmt.Fprintln(out, "Resource not observed in active snapshot."); err != nil {
			return err
		}
	}
	if len(data.References) == 0 {
		message := "0 references found"
		if !data.Coverage.Complete {
			message = "0 observed references; result incomplete"
		}
		if _, err := fmt.Fprintln(out, message); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(out, "SOURCE\tRELATION\tCONDITION\tPROFILE(S)"); err != nil {
			return err
		}
		for _, edge := range data.References {
			label := edge.Source.Name
			if label == "" {
				label = edge.Source.ID
			}
			profiles := make([]string, 0, len(edge.Observers))
			for _, observer := range edge.Observers {
				profiles = append(profiles, observer.Profile)
			}
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", safeAWSQueryText(label), safeAWSQueryText(edge.RelationType), safeAWSQueryText(edge.Condition), safeAWSQueryText(strings.Join(profiles, ","))); err != nil {
				return err
			}
		}
	}
	if data.Truncated {
		if _, err := fmt.Fprintf(out, "Results: first %d references shown (truncated)\n", data.Limit); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "Coverage: %d succeeded, %d failed, %d not observed\n", data.Coverage.Succeeded, data.Coverage.Failed, data.Coverage.NotObserved); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Completeness: rule-references=%t attachments=%t\n", data.Coverage.RuleReferencesComplete, data.Coverage.AttachmentsComplete); err != nil {
		return err
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintln(out, "Warning:", safeAWSQueryText(warning)); err != nil {
			return err
		}
	}
	return nil
}

func mapAWSSnapshotFailure(err error, message string) error {
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		return commandErr
	}
	return &CommandError{Code: "operational_error", Message: message, Exit: ExitOperational}
}
