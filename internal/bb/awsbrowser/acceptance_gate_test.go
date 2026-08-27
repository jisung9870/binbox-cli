package awsbrowser_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/integration"
)

// gateCLI is a controlled stand-in for the AWS CLI boundary. Search profile
// discovery may use ListProfiles, while resource results must come through the
// credential-free SearchCore query seam rather than a subprocess.
type gateCLI struct {
	mu          sync.Mutex
	listCalls   int
	exportCalls int
}

func (cli *gateCLI) ListProfiles(context.Context, []string) ([]string, error) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	cli.listCalls++
	return []string{"current", "slow-secondary"}, nil
}

func (cli *gateCLI) ExportCredentials(context.Context, string, []string) ([]byte, error) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	cli.exportCalls++
	return nil, nil
}

func (cli *gateCLI) counts() (int, int) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	return cli.listCalls, cli.exportCalls
}

type gateSearchCore struct {
	secondaryRelease <-chan struct{}
	firstResult      chan struct{}
	firstOnce        sync.Once

	mu      sync.Mutex
	queries []integration.Request
}

func (core *gateSearchCore) Resolve(ctx context.Context, request integration.ContextRequest) (integration.ContextResult, error) {
	if request.Profile == "slow-secondary" {
		select {
		case <-core.secondaryRelease:
		case <-ctx.Done():
			return integration.ContextResult{}, ctx.Err()
		}
	}
	account := "100000000001"
	if request.Profile == "slow-secondary" {
		account = "100000000002"
	}
	identity := awsbrowser.VerifiedIdentity{
		Partition: "aws", AccountID: account,
		PrincipalARN:         "arn:aws:iam::" + account + ":role/Acceptance",
		CredentialGeneration: 1,
	}
	awsContext, err := awsbrowser.NewAWSContext(awsbrowser.ContextSpec{
		Mode: awsbrowser.ContextModeNamedProfile, Profile: request.Profile, Region: request.Region,
	}, identity, "Acceptance")
	if err != nil {
		return integration.ContextResult{}, err
	}
	return integration.ContextResult{Context: &awsContext, Coverage: integration.Coverage{ContextResolved: true}}, nil
}

func (core *gateSearchCore) Query(_ context.Context, request integration.Request) (integration.Result, error) {
	core.mu.Lock()
	core.queries = append(core.queries, request)
	core.mu.Unlock()
	defer core.firstOnce.Do(func() { close(core.firstResult) })
	return integration.Result{Update: integration.Update{
		Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadEmpty},
		Coverage: integration.Coverage{ContextResolved: true, QueryStarted: true, Completed: true},
	}}, nil
}

func (core *gateSearchCore) queryCount() int {
	core.mu.Lock()
	defer core.mu.Unlock()
	return len(core.queries)
}

func (core *gateSearchCore) queryOperations() []string {
	core.mu.Lock()
	defer core.mu.Unlock()
	operations := make([]string, len(core.queries))
	for index := range core.queries {
		operations[index] = core.queries[index].Operation
	}
	return operations
}

// This is deterministic scheduling and boundary evidence, not a benchmark and
// not a substitute for the approved 12-profile real-AWS latency/CloudTrail run.
func TestDomainAndRoleFirstResultPrecedesBlockedSecondaryProfilesWithoutResourceSubprocess(t *testing.T) {
	for _, test := range []struct {
		name, query, operation string
		kind                   integration.SearchKind
	}{
		{name: "domain", kind: integration.SearchDomain, query: "api.example.com", operation: awsbrowser.OperationListHostedZones},
		{name: "role", kind: integration.SearchRole, query: "worker", operation: awsbrowser.OperationGetRole},
	} {
		t.Run(test.name, func(t *testing.T) {
			secondaryRelease := make(chan struct{})
			core := &gateSearchCore{secondaryRelease: secondaryRelease, firstResult: make(chan struct{})}
			cli := new(gateCLI)
			service, err := integration.NewSearchService(core, cli, []string{"SAFE_GATE=1"})
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			started := time.Now()
			go func() {
				_, submitErr := service.Submit(context.Background(), integration.SearchRequest{
					Kind: test.kind, Scope: integration.SearchAll, Query: test.query,
					Profile: "current", Region: "ap-northeast-2",
				})
				done <- submitErr
			}()

			select {
			case <-core.firstResult:
				if elapsed := time.Since(started); elapsed >= time.Second {
					t.Fatalf("controlled first result took %s, want under 1s", elapsed)
				}
			case <-time.After(time.Second):
				t.Fatal("current-profile result waited for a blocked secondary profile")
			}

			select {
			case err := <-done:
				t.Fatalf("all-scope search completed before the secondary profile was released: %v", err)
			default:
			}
			close(secondaryRelease)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("controlled search did not finish")
			}

			listCalls, exportCalls := cli.counts()
			if listCalls != 1 || exportCalls != 0 || core.queryCount() != 2 {
				t.Fatalf("CLI list/export calls=%d/%d, SDK-seam queries=%d", listCalls, exportCalls, core.queryCount())
			}
			for _, operation := range core.queryOperations() {
				if operation != test.operation {
					t.Fatalf("resource query operation=%q want %q", operation, test.operation)
				}
			}
		})
	}
}
