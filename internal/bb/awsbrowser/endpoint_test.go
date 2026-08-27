package awsbrowser

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type endpointGuardTransport struct {
	poisonHost string
	mu         sync.Mutex
	seen       []string
}

func (g *endpointGuardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.seen = append(g.seen, request.URL.String())
	g.mu.Unlock()
	if request.URL.Host == g.poisonHost {
		return http.DefaultTransport.RoundTrip(request)
	}
	return &http.Response{
		StatusCode: http.StatusTeapot,
		Status:     "418 outbound request blocked by test",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func (g *endpointGuardTransport) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = nil
}

func (g *endpointGuardTransport) URLs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...)
}

func TestSDKRuntimeIgnoresConfiguredEndpoints(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		configBody  func(string) string
	}{
		{
			name: "global environment endpoint",
			environment: map[string]string{
				"AWS_ENDPOINT_URL": "{POISON}",
			},
		},
		{
			name: "service environment endpoints",
			environment: map[string]string{
				"AWS_ENDPOINT_URL_STS":      "{POISON}",
				"AWS_ENDPOINT_URL_EC2":      "{POISON}",
				"AWS_ENDPOINT_URL_IAM":      "{POISON}",
				"AWS_ENDPOINT_URL_ROUTE_53": "{POISON}",
			},
		},
		{
			name: "profile endpoint",
			configBody: func(endpoint string) string {
				return "[profile poison]\nregion = us-east-1\nendpoint_url = " + endpoint + "\n"
			},
		},
		{
			name: "profile services endpoints",
			configBody: func(endpoint string) string {
				return "[profile poison]\nregion = us-east-1\nservices = poison-services\n\n" +
					"[services poison-services]\n" +
					"sts =\n  endpoint_url = " + endpoint + "\n" +
					"ec2 =\n  endpoint_url = " + endpoint + "\n" +
					"iam =\n  endpoint_url = " + endpoint + "\n" +
					"route_53 =\n  endpoint_url = " + endpoint + "\n"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var poisonRequests atomic.Int32
			listener := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				poisonRequests.Add(1)
				response.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(response, "<Response/>")
			}))
			defer listener.Close()

			poisonURL, err := url.Parse(listener.URL)
			if err != nil {
				t.Fatal(err)
			}
			guard := &endpointGuardTransport{poisonHost: poisonURL.Host}
			configureEndpointEnvironment(t, test.environment, listener.URL, test.configBody)

			provider := testCredentialProvider(t)
			load := guardedConfigLoader(&http.Client{Transport: guard})
			assertPoisonFixtureActive(t, provider, load, poisonRequests.Load)

			poisonRequests.Store(0)
			guard.reset()
			runtime, err := newSDKRuntime(context.Background(), "us-east-1", provider, load)
			if err != nil {
				t.Fatal(err)
			}
			invokeEveryService(t, runtime)
			if got := poisonRequests.Load(); got != 0 {
				t.Fatalf("poison listener received %d requests; URLs=%v", got, guard.URLs())
			}
			for _, rawURL := range guard.URLs() {
				seenURL, err := url.Parse(rawURL)
				if err != nil {
					t.Fatal(err)
				}
				if seenURL.Host == poisonURL.Host {
					t.Fatalf("configured endpoint reached: %s", rawURL)
				}
			}
		})
	}
}

func configureEndpointEnvironment(t *testing.T, environment map[string]string, endpoint string, configBody func(string) string) {
	t.Helper()
	for _, name := range []string{
		"AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_STS",
		"AWS_ENDPOINT_URL_EC2",
		"AWS_ENDPOINT_URL_IAM",
		"AWS_ENDPOINT_URL_ROUTE_53",
	} {
		t.Setenv(name, "")
	}
	for name, value := range environment {
		t.Setenv(name, strings.ReplaceAll(value, "{POISON}", endpoint))
	}

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config")
	body := "[profile poison]\nregion = us-east-1\n"
	if configBody != nil {
		body = configBody(endpoint)
	}
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(directory, "credentials"))
	t.Setenv("AWS_PROFILE", "poison")
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func guardedConfigLoader(client *http.Client) configLoader {
	return func(ctx context.Context, options ...func(*config.LoadOptions) error) (aws.Config, error) {
		options = append(options, config.WithHTTPClient(client))
		return config.LoadDefaultConfig(ctx, options...)
	}
}

func assertPoisonFixtureActive(t *testing.T, provider *CredentialProvider, load configLoader, count func() int32) {
	t.Helper()
	cfg, err := load(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(provider),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw := &sdkRuntime{
		sts:     sts.NewFromConfig(cfg),
		ec2:     ec2.NewFromConfig(cfg),
		iam:     iam.NewFromConfig(cfg),
		route53: route53.NewFromConfig(cfg),
	}
	invokeEveryService(t, raw)
	if got := count(); got != 4 {
		t.Fatalf("poison fixture reached listener %d times, want 4", got)
	}
}

func invokeEveryService(t *testing.T, runtime *sdkRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	calls := []func() error{
		func() error {
			_, err := runtime.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			return err
		},
		func() error {
			_, err := runtime.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
			return err
		},
		func() error {
			_, err := runtime.iam.ListRoles(ctx, &iam.ListRolesInput{})
			return err
		},
		func() error {
			_, err := runtime.route53.ListHostedZones(ctx, &route53.ListHostedZonesInput{})
			return err
		},
	}
	for _, call := range calls {
		if err := call(); errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
}
