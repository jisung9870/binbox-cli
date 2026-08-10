package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tfxTestApp(t *testing.T) (*App, *bytes.Buffer, string) {
	t.Helper()
	a, out, _, state := testApp(t)
	a.env = append(a.env, "GO_WANT_BB_TFX_HELPER=1")
	a.lookPath = func(string) (string, error) { return "helper", nil }
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestTFXHelperProcess", "--", name}, args...)...)
	}
	return a, out, state
}

func TestTFXHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BB_TFX_HELPER") != "1" {
		return
	}
	args := os.Args
	start := 0
	for i, arg := range args {
		if arg == "--" {
			start = i + 1
			break
		}
	}
	if start == 0 || start >= len(args) {
		os.Exit(92)
	}
	name, argv := args[start], args[start+1:]
	if name == "terraform" && len(argv) > 0 && argv[0] == "plan" {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-out=") {
				path := strings.TrimPrefix(arg, "-out=")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					os.Exit(93)
				}
				if err := os.WriteFile(path, []byte("plan"), 0o600); err != nil {
					os.Exit(94)
				}
			}
		}
	}
	if name == "terraform" && len(argv) >= 2 && argv[0] == "show" && argv[1] == "-json" {
		fmt.Fprint(os.Stdout, `{"planned_values":{}}`)
		os.Exit(0)
	}
	if name == "aws" && len(argv) >= 2 && argv[0] == "sts" && argv[1] == "get-caller-identity" {
		fmt.Fprint(os.Stdout, "123456789012\tarn:aws:iam::123456789012:user/test\n")
		os.Exit(0)
	}
	if name == "tf-summarize" {
		input, _ := os.ReadFile("/dev/stdin")
		fmt.Fprintf(os.Stdout, "tf-summarize args=%s input=%s", strings.Join(argv, ","), input)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stdout, "%s args=%s", name, strings.Join(argv, ","))
	os.Exit(0)
}

func TestTFXHelpAndSafeBoundary(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	if err := a.Run([]string{"tfx"}); err != nil || !strings.Contains(out.String(), "interactive confirmation") {
		t.Fatalf("help err=%v output=%q", err, out.String())
	}
	out.Reset()
}

func writeTFXSession(t *testing.T, state string, expiry int64, account, scope string) string {
	t.Helper()
	path := filepath.Join(state, "binbox", "tfsession")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d\t%s", expiry, account)
	if scope != "" {
		line += "\t" + scope
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTFXSessionWritesExactLegacyTSVAtomicallyWithMode(t *testing.T) {
	a, out, state := tfxTestApp(t)
	a.in = strings.NewReader("9012\n")
	if err := a.Run([]string{"tfx", "session", "5", "--destroy"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "binbox", "tfsession")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "1786320300\t123456789012\tdestroy\n"; got != want {
		t.Fatalf("session=%q want=%q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "****9012") {
		t.Fatalf("missing redacted prompt: %q", out.String())
	}
}

func TestTFXSessionRejectsBadConfirmationWithoutWrite(t *testing.T) {
	a, _, state := tfxTestApp(t)
	a.in = strings.NewReader("0000\n")
	if err := a.Run([]string{"tfx", "session"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "binbox", "tfsession")); !os.IsNotExist(err) {
		t.Fatalf("session created: %v", err)
	}
}

func TestTFXApplyGuardsAndDirectArgv(t *testing.T) {
	a, out, state := tfxTestApp(t)
	path := writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "") // 2-column is apply.
	plan := filepath.Join(t.TempDir(), "plan.out")
	if err := os.WriteFile(plan, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "TFPLAN_FILE="+plan)
	a.in = strings.NewReader("yes\n")
	if err := a.Run([]string{"tfx", "apply", "-no-color"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "terraform args=apply,-no-color,"+plan) {
		t.Fatalf("apply=%q", out.String())
	}
	// Expiry rejects without deleting the cross-version legacy session.
	out.Reset()
	if err := os.WriteFile(path, []byte("1\t123456789012\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"tfx", "apply"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("expired err=%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expired was deleted: %v", err)
	}
}

func TestTFXApplyRefusesAccountMismatchAndMissingPlan(t *testing.T) {
	a, _, state := tfxTestApp(t)
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "999999999999", "apply")
	if err := a.Run([]string{"tfx", "apply"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("mismatch=%v", err)
	}
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "apply")
	a.env = append(a.env, "TFPLAN_FILE="+filepath.Join(t.TempDir(), "missing"))
	if err := a.Run([]string{"tfx", "apply"}); err == nil || ExitCode(err) != ExitOperational {
		t.Fatalf("missing plan=%v", err)
	}
}

func TestTFXDestroyGuardsPlanAndConfirms(t *testing.T) {
	a, out, state := tfxTestApp(t)
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "destroy")
	plan := filepath.Join(t.TempDir(), "destroy.out")
	a.env = append(a.env, "TFDESTROY_PLAN_FILE="+plan)
	a.in = strings.NewReader("y\n")
	if err := a.Run([]string{"tfx", "destroy", "-var-file=qa.tfvars"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "terraform args=plan,-destroy,-out="+plan+",-var-file=qa.tfvars") || !strings.Contains(out.String(), "terraform args=apply,"+plan) {
		t.Fatalf("destroy=%q", out.String())
	}
	out.Reset()
	if err := a.Run([]string{"tfx", "destroy", "-auto-approve"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("auto approve=%v", err)
	}
	for _, arg := range []string{"-out=other.plan", "-out", "-destroy=false"} {
		if err := a.Run([]string{"tfx", "destroy", arg}); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("owned flag %q err=%v", arg, err)
		}
	}
}

func TestTFXDestroyRequiresDestroyScopeAndEndDeletes(t *testing.T) {
	a, _, state := tfxTestApp(t)
	path := writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "apply")
	if err := a.Run([]string{"tfx", "destroy"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("scope=%v", err)
	}
	if err := a.Run([]string{"tfx", "end"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session remains: %v", err)
	}
}

func TestTFXTerraformPassThroughAndPlanSafety(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	if err := a.Run([]string{"tfx", "init", "-upgrade"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "terraform args=init,-upgrade" {
		t.Fatalf("init=%q", got)
	}
	out.Reset()
	plan := filepath.Join(t.TempDir(), "safe.tfplan")
	a.env = append(a.env, "TFPLAN_FILE="+plan)
	if err := a.Run([]string{"tfx", "plan", "-refresh=false"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "terraform args=plan,-out="+plan+",-refresh=false" {
		t.Fatalf("plan=%q", got)
	}
	if err := a.Run([]string{"tfx", "plan", "-out=caller.tfplan"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("out override err=%v exit=%d", err, ExitCode(err))
	}
	if err := a.Run([]string{"tfx", "plan", "-destroy"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("destroy override err=%v exit=%d", err, ExitCode(err))
	}
}

func TestTFXSumUsesDirectPipeline(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	plan := filepath.Join(t.TempDir(), "plan.out")
	if err := os.WriteFile(plan, []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"tfx", "sum", "draw", plan}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "tf-summarize args=-tree,-draw") || !strings.Contains(got, "planned_values") {
		t.Fatalf("sum=%q", got)
	}
	out.Reset()
	if err := a.Run([]string{"tfx", "sum", "md", "summary.md", plan}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tf-summarize args=-md,-out=summary.md") {
		t.Fatalf("md=%q", out.String())
	}
}

func TestTFXStatusReadsLegacySessionWithoutMutationAndRedactsJSON(t *testing.T) {
	a, out, state := tfxTestApp(t)
	path := filepath.Join(state, "binbox", "tfsession")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "1786406400\t123456789012\tdestroy\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"tfx", "status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "123456789012") || !strings.Contains(out.String(), "9012") {
		t.Fatalf("status leaked account: %s", out.String())
	}
	var response struct {
		Data tfxSessionStatus `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.State != "valid" || response.Data.Scope != "destroy" {
		t.Fatalf("status=%+v", response.Data)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != contents {
		t.Fatalf("session changed after status: %q err=%v", after, err)
	}
	// Expiry is also informational: status must leave the legacy file intact.
	out.Reset()
	if err := os.WriteFile(path, []byte("1\t123456789012\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"tfx", "status", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state":"expired"`) {
		t.Fatalf("expired status=%s", out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expired session was changed/deleted: %v", err)
	}
}

func TestTFXPlainStatusPreservesLegacyFailureExit(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	err := a.Run([]string{"tfx", "status"})
	if ExitCode(err) != ExitOperational || strings.TrimSpace(out.String()) != "tfx session missing" {
		t.Fatalf("err=%v exit=%d output=%q", err, ExitCode(err), out.String())
	}
}

func TestTFXStateListOnly(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	if err := a.Run([]string{"tfx", "state", "list", "-state=remote.tfstate"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "terraform args=state,list,-state=remote.tfstate" {
		t.Fatalf("state list=%q", got)
	}
	if err := a.Run([]string{"tfx", "state", "rm", "aws_x.y"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("state rm err=%v exit=%d", err, ExitCode(err))
	}
}
