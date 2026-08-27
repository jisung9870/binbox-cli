package awsbrowser

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// These profiles represent the credential topologies that the AWS CLI resolves
// before the browser receives a process-format credential document. The test
// deliberately does not parse, execute, or otherwise expand their source
// chains: that remains inside the AWS CLI credential trust boundary.
type phase0Profile struct {
	name     string
	topology string
	account  string
}

var phase0Profiles = []phase0Profile{
	{name: "static-engineering", topology: "static", account: "100000000001"},
	{name: "static-operations", topology: "static", account: "100000000002"},
	{name: "static-audit", topology: "static", account: "100000000003"},
	{name: "sso-engineering", topology: "sso", account: "100000000004"},
	{name: "sso-operations", topology: "sso", account: "100000000005"},
	{name: "sso-audit", topology: "sso", account: "100000000006"},
	{name: "role-engineering", topology: "role/source_profile", account: "100000000007"},
	{name: "role-operations", topology: "role/source_profile", account: "100000000008"},
	{name: "role-audit", topology: "role/source_profile", account: "100000000009"},
	{name: "process-engineering", topology: "credential_process", account: "100000000010"},
	{name: "process-operations", topology: "credential_process", account: "100000000011"},
	{name: "process-audit", topology: "credential_process", account: "100000000012"},
}

// phase0CLI is a deterministic fake of the only browser credential capability.
// It retains profile names and sanitized environments, never credentials.
type phase0CLI struct {
	mu       sync.Mutex
	profiles map[string]phase0Profile
	calls    map[string]int
	envs     map[string][][]string
	block    bool
	started  chan struct{}
	once     sync.Once
	err      error
}

func newPhase0CLI(profiles []phase0Profile) *phase0CLI {
	byName := make(map[string]phase0Profile, len(profiles))
	for _, profile := range profiles {
		byName[profile.name] = profile
	}
	return &phase0CLI{
		profiles: byName,
		calls:    make(map[string]int, len(profiles)),
		envs:     make(map[string][][]string, len(profiles)),
	}
}

func (c *phase0CLI) ExportCredentials(ctx context.Context, profile string, env []string) ([]byte, error) {
	if c.block {
		c.once.Do(func() { close(c.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c.mu.Lock()
	c.calls[profile]++
	c.envs[profile] = append(c.envs[profile], append([]string(nil), env...))
	err := c.err
	_, ok := c.profiles[profile]
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("unexpected fake profile")
	}
	return []byte(`{"Version":1,"AccessKeyId":"phase0-access-key","SecretAccessKey":"phase0-secret"}`), nil
}

func (c *phase0CLI) callCount(profile string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[profile]
}

func (c *phase0CLI) environments(profile string) [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([][]string, len(c.envs[profile]))
	for index := range result {
		result[index] = append([]string(nil), c.envs[profile][index]...)
	}
	return result
}

func TestPhase0NamedProfileCredentialMatrix(t *testing.T) {
	cli := newPhase0CLI(phase0Profiles)
	baseEnv := []string{
		"SAFE_PHASE0=preserved",
		"AWS_PROFILE=ambient-profile",
		"AWS_ACCESS_KEY_ID=ambient-access-key",
		"AWS_SECRET_ACCESS_KEY=ambient-secret",
		"AWS_SESSION_TOKEN=ambient-token",
		"AWS_ENDPOINT_URL=http://127.0.0.1:1",
		"AWS_ENDPOINT_URL_STS=http://127.0.0.1:2",
	}

	var mu sync.Mutex
	providers := make(map[string]*CredentialProvider, len(phase0Profiles))
	caches := make(map[string]*aws.CredentialsCache, len(phase0Profiles))
	validated := make(map[string]int, len(phase0Profiles))
	factory, err := newRuntimeFactory(cli, baseEnv, func(_ context.Context, _ string, provider *CredentialProvider) (*sdkRuntime, error) {
		profile := cli.profiles[provider.profile]
		runtime, _ := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", profile.account, "phase0-"+profile.name)
		}, nil)
		mu.Lock()
		providers[provider.profile] = provider
		caches[provider.profile] = runtime.credentials
		mu.Unlock()
		return runtime, nil
	}, func(_ context.Context, profile string, _ []string) error {
		if _, ok := cli.profiles[profile]; !ok {
			return errors.New("unexpected named profile source validation")
		}
		mu.Lock()
		validated[profile]++
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	seenAccounts := make(map[string]string, len(phase0Profiles))
	for _, profile := range phase0Profiles {
		runtime, err := factory.Resolve(context.Background(), ContextSpec{
			Mode: ContextModeNamedProfile, Profile: profile.name, Region: "ap-northeast-2",
		})
		if err != nil {
			t.Fatalf("%s (%s): %v", profile.name, profile.topology, err)
		}
		identity := runtime.Identity()
		if identity.AccountID != profile.account || identity.CredentialGeneration != 1 {
			t.Fatalf("%s identity=%+v", profile.name, identity)
		}
		if previous, exists := seenAccounts[identity.AccountID]; exists {
			t.Fatalf("profiles %s and %s share fake account %s", previous, profile.name, identity.AccountID)
		}
		seenAccounts[identity.AccountID] = profile.name
	}

	if len(providers) != len(phase0Profiles) || len(caches) != len(phase0Profiles) {
		t.Fatalf("provider/cache graphs=%d/%d want %d", len(providers), len(caches), len(phase0Profiles))
	}
	for _, profile := range phase0Profiles {
		if providers[profile.name] == nil || caches[profile.name] == nil {
			t.Fatalf("missing graph for %s", profile.name)
		}
		if validated[profile.name] != 1 {
			t.Fatalf("%s source validation calls=%d want 1", profile.name, validated[profile.name])
		}
		for _, other := range phase0Profiles {
			if profile.name == other.name {
				continue
			}
			if providers[profile.name] == providers[other.name] || caches[profile.name] == caches[other.name] {
				t.Fatalf("profile graphs are shared: %s and %s", profile.name, other.name)
			}
		}
		for _, env := range cli.environments(profile.name) {
			assertPhase0NamedEnvironment(t, profile.name, env)
		}
	}
}

func TestPhase0NamedProfileTopologyFixtures(t *testing.T) {
	var config strings.Builder
	expected := make(map[string]ProfileSourceKind, len(phase0Profiles))
	for _, profile := range phase0Profiles {
		config.WriteString("[profile " + profile.name + "]\n")
		switch profile.topology {
		case "static":
			config.WriteString("aws_access_key_id = fixture\naws_secret_access_key = fixture\n")
			expected[profile.name] = ProfileSourceStatic
		case "sso":
			config.WriteString("sso_session = phase0\n")
			expected[profile.name] = ProfileSourceSSO
		case "role/source_profile":
			source := "source-" + profile.name
			config.WriteString("role_arn = arn:aws:iam::" + profile.account + ":role/Phase0\nsource_profile = " + source + "\n")
			config.WriteString("[profile " + source + "]\naws_access_key_id = fixture\naws_secret_access_key = fixture\n")
			expected[profile.name] = ProfileSourceRole
		case "credential_process":
			config.WriteString("credential_process = phase0-fixture\n")
			expected[profile.name] = ProfileSourceCredentialProcess
		default:
			t.Fatalf("unknown topology %q", profile.topology)
		}
	}
	for _, profile := range phase0Profiles {
		source, err := ClassifyProfileSource(profile.name, []byte(config.String()), nil)
		if err != nil || source.Kind != expected[profile.name] {
			t.Fatalf("%s topology=%s source=%+v err=%v", profile.name, profile.topology, source, err)
		}
	}
}

func TestPhase0ConcurrentCredentialCachesExportOncePerLifetime(t *testing.T) {
	cli := newPhase0CLI(phase0Profiles)
	var workers sync.WaitGroup
	for _, profile := range phase0Profiles {
		provider, err := NewCredentialProvider(cli, profile.name, []string{"SAFE_PHASE0=preserved"})
		if err != nil {
			t.Fatal(err)
		}
		cache := aws.NewCredentialsCache(provider)
		for attempt := 0; attempt < 16; attempt++ {
			workers.Add(1)
			go func(profile string, cache *aws.CredentialsCache) {
				defer workers.Done()
				if _, err := cache.Retrieve(context.Background()); err != nil {
					t.Errorf("%s retrieve: %v", profile, err)
				}
			}(profile.name, cache)
		}
	}
	workers.Wait()

	for _, profile := range phase0Profiles {
		if got := cli.callCount(profile.name); got != 1 {
			t.Errorf("%s exports=%d want exactly one per cache lifetime", profile.name, got)
		}
	}
}

func TestPhase0ExpiredSSOIsTypedPartialFailure(t *testing.T) {
	profiles := []phase0Profile{{name: "sso-expired", topology: "sso", account: "100000000099"}}
	cli := newPhase0CLI(profiles)
	cli.err = &CLIError{Kind: CLIAuthRequired, Operation: cliOperationExportCredentials, Code: "SSOTokenLoadError"}
	provider, err := NewCredentialProvider(cli, "sso-expired", []string{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Retrieve(context.Background())
	var credentialError *CredentialError
	if !errors.As(err, &credentialError) || credentialError.Kind != CredentialAuthRequired || credentialError.Code != "SSOTokenLoadError" {
		t.Fatalf("expired SSO error=%v", err)
	}
}

func TestPhase0NamedProfileCancellationStopsCredentialExport(t *testing.T) {
	cli := newPhase0CLI([]phase0Profile{{name: "sso-cancelled", topology: "sso", account: "100000000098"}})
	cli.block = true
	cli.started = make(chan struct{})
	provider, err := NewCredentialProvider(cli, "sso-cancelled", []string{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := provider.Retrieve(ctx)
		done <- err
	}()
	select {
	case <-cli.started:
	case <-time.After(2 * time.Second):
		t.Fatal("credential export did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("credential export did not stop after cancellation")
	}
}

func TestPhase0CLIHasZeroResourceDataCapabilities(t *testing.T) {
	cliType := reflect.TypeOf((*CLI)(nil)).Elem()
	methods := make([]string, cliType.NumMethod())
	for index := range methods {
		methods[index] = cliType.Method(index).Name
	}
	sort.Strings(methods)
	if want := []string{"ExportCredentials", "ListProfiles"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("CLI capabilities=%v want only credential/profile control plane", methods)
	}
}

// TestPhase0DeferredDomainRoleTTFAndQueryLayerZeroSubprocessEvidence is
// intentionally skipped: it needs real profile/domain fixtures and the later
// provider/query layer, neither of which exists in this Phase 0 runtime seam.
// A passing fake runtime test must not be read as evidence for either gate.
func TestPhase0DeferredDomainRoleTTFAndQueryLayerZeroSubprocessEvidence(t *testing.T) {
	t.Skip("deferred: requires provider/query layer and approved real-profile latency fixture")
}

func assertPhase0NamedEnvironment(t *testing.T, profile string, env []string) {
	t.Helper()
	values := make(map[string]string, len(env))
	for _, entry := range env {
		name, value, ok := splitPhase0Environment(entry)
		if !ok {
			t.Fatalf("%s retained malformed environment entry %q", profile, entry)
		}
		values[name] = value
	}
	if values["SAFE_PHASE0"] != "preserved" || values["AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"] != "true" {
		t.Fatalf("%s isolated environment=%v", profile, values)
	}
	for name := range namedProfileIdentityEnv {
		if _, ok := values[name]; ok {
			t.Fatalf("%s retained named-profile identity variable %s", profile, name)
		}
	}
	for name := range values {
		if name == "AWS_ENDPOINT_URL" || len(name) > len("AWS_ENDPOINT_URL_") && name[:len("AWS_ENDPOINT_URL_")] == "AWS_ENDPOINT_URL_" {
			t.Fatalf("%s retained endpoint override %s", profile, name)
		}
	}
}

func splitPhase0Environment(entry string) (string, string, bool) {
	for index := range entry {
		if entry[index] == '=' {
			return entry[:index], entry[index+1:], index > 0
		}
	}
	return "", "", false
}
