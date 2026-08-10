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
	tfargv := argv
	hadChdir := false
	if len(tfargv) > 0 && strings.HasPrefix(tfargv[0], "-chdir=") {
		hadChdir = true
		tfargv = tfargv[1:]
	}
	if name == "terraform" && len(tfargv) > 0 && tfargv[0] == "plan" {
		for _, arg := range tfargv[1:] {
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
	if name == "terraform" && len(argv) >= 2 && argv[0] == "state" && argv[1] == "list" {
		fmt.Fprint(os.Stdout, "aws_instance.example\naws_s3_bucket.logs\n")
		os.Exit(0)
	}
	if name == "terraform" && len(tfargv) >= 2 && tfargv[0] == "show" && tfargv[1] == "-json" {
		if hadChdir {
			fmt.Fprint(os.Stdout, `{"resource_changes":[]}`)
		} else {
			fmt.Fprint(os.Stdout, `{"planned_values":{}}`)
		}
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
	if name == "terraform" && len(argv) >= 2 && argv[0] == "apply" {
		plan, err := os.ReadFile(argv[len(argv)-1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "read applied plan: %v", err)
			os.Exit(95)
		}
		fmt.Fprintf(os.Stdout, "terraform args=%s plan-content=%s", strings.Join(argv, ","), plan)
		if os.Getenv("GO_WANT_BB_TFX_FAIL_APPLY") == "1" {
			os.Exit(96)
		}
		os.Exit(0)
	}
	fmt.Fprintf(os.Stdout, "%s args=%s", name, strings.Join(argv, ","))
	os.Exit(0)
}

// replaceOnFirstRead models an attacker or concurrent process replacing the
// source path after bb has made its private copy, but before the user answers.
type replaceOnFirstRead struct {
	reader      *strings.Reader
	source      string
	replacement []byte
	done        bool
}

func (r *replaceOnFirstRead) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		tmp := r.source + ".replacement"
		if err := os.WriteFile(tmp, r.replacement, 0o600); err != nil {
			return 0, err
		}
		if err := os.Rename(tmp, r.source); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(p)
}

func assertNoTFXSnapshotArtifacts(t *testing.T, state string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "bb"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tfx-plan-") {
			t.Fatalf("private snapshot artifact remains: %s", entry.Name())
		}
	}
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
	if !strings.Contains(out.String(), "terraform args=apply,-no-color,") || strings.Contains(out.String(), "terraform args=apply,-no-color,"+plan) || !strings.Contains(out.String(), "plan-content=") {
		t.Fatalf("apply=%q", out.String())
	}
	assertNoTFXSnapshotArtifacts(t, state)
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
	if !strings.Contains(out.String(), "terraform args=plan,-destroy,-out="+plan+",-var-file=qa.tfvars") || !strings.Contains(out.String(), "terraform args=apply,") || strings.Contains(out.String(), "terraform args=apply,"+plan) || !strings.Contains(out.String(), "plan-content=plan") {
		t.Fatalf("destroy=%q", out.String())
	}
	assertNoTFXSnapshotArtifacts(t, state)
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

func TestTFXApplyUsesImmutablePrivateSnapshotAfterSourceReplacement(t *testing.T) {
	a, out, state := tfxTestApp(t)
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "apply")
	plan := filepath.Join(t.TempDir(), "apply.tfplan")
	if err := os.WriteFile(plan, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "TFPLAN_FILE="+plan)
	a.in = &replaceOnFirstRead{reader: strings.NewReader("yes\n"), source: plan, replacement: []byte("evil")}
	if err := a.Run([]string{"tfx", "apply"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Apply saved plan \""+plan+"\" (sha256 ") || !strings.Contains(got, "plan-content=trusted") || strings.Contains(got, "terraform args=apply,"+plan) {
		t.Fatalf("immutable apply handoff failed: %q", got)
	}
	if b, err := os.ReadFile(plan); err != nil || string(b) != "evil" {
		t.Fatalf("source was not replaced as expected: %q err=%v", b, err)
	}
	assertNoTFXSnapshotArtifacts(t, state)
}

func TestTFXApplyRejectsSymlinkPlanWithoutSnapshotArtifacts(t *testing.T) {
	a, _, state := tfxTestApp(t)
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "apply")
	dir := t.TempDir()
	target := filepath.Join(dir, "target.tfplan")
	link := filepath.Join(dir, "linked.tfplan")
	if err := os.WriteFile(target, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	a.env = append(a.env, "TFPLAN_FILE="+link)
	if err := a.Run([]string{"tfx", "apply"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("symlink err=%v", err)
	}
	assertNoTFXSnapshotArtifacts(t, state)
}

func TestTFXApplyCleansPrivateSnapshotOnCancelAndTerraformFailure(t *testing.T) {
	plan := filepath.Join(t.TempDir(), "apply.tfplan")
	if err := os.WriteFile(plan, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}

	cancelled, _, cancelState := tfxTestApp(t)
	writeTFXSession(t, cancelState, cancelled.now().Add(time.Minute).Unix(), "123456789012", "apply")
	cancelled.env = append(cancelled.env, "TFPLAN_FILE="+plan)
	cancelled.in = strings.NewReader("no\n")
	if err := cancelled.Run([]string{"tfx", "apply"}); err != nil {
		t.Fatalf("cancelled apply: %v", err)
	}
	assertNoTFXSnapshotArtifacts(t, cancelState)

	failing, _, failState := tfxTestApp(t)
	writeTFXSession(t, failState, failing.now().Add(time.Minute).Unix(), "123456789012", "apply")
	failing.env = append(failing.env, "TFPLAN_FILE="+plan, "GO_WANT_BB_TFX_FAIL_APPLY=1")
	failing.in = strings.NewReader("yes\n")
	if err := failing.Run([]string{"tfx", "apply"}); err == nil {
		t.Fatal("expected Terraform failure")
	}
	assertNoTFXSnapshotArtifacts(t, failState)
	if b, err := os.ReadFile(plan); err != nil || string(b) != "trusted" {
		t.Fatalf("source changed during handoff: %q err=%v", b, err)
	}
}

func TestTFXDestroyUsesImmutablePrivateSnapshotAfterSourceReplacement(t *testing.T) {
	a, out, state := tfxTestApp(t)
	writeTFXSession(t, state, a.now().Add(time.Minute).Unix(), "123456789012", "destroy")
	plan := filepath.Join(t.TempDir(), "destroy.tfplan")
	a.env = append(a.env, "TFDESTROY_PLAN_FILE="+plan)
	a.in = &replaceOnFirstRead{reader: strings.NewReader("y\n"), source: plan, replacement: []byte("evil")}
	if err := a.Run([]string{"tfx", "destroy"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Apply destroy plan \""+plan+"\" (sha256 ") || !strings.Contains(got, "plan-content=plan") || strings.Contains(got, "terraform args=apply,"+plan) {
		t.Fatalf("immutable destroy handoff failed: %q", got)
	}
	if b, err := os.ReadFile(plan); err != nil || string(b) != "evil" {
		t.Fatalf("destroy source was not replaced as expected: %q err=%v", b, err)
	}
	assertNoTFXSnapshotArtifacts(t, state)
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

func TestTFXStateCommandsReobserveAndUseDirectArgv(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	if err := a.Run([]string{"tfx", "state", "list", "-state=remote.tfstate"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "aws_instance.example\naws_s3_bucket.logs\n" {
		t.Fatalf("state list=%q", got)
	}
	out.Reset()
	if err := a.Run([]string{"tfx", "state", "show", "aws_instance.example"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "terraform args=state,show,aws_instance.example" {
		t.Fatalf("state show=%q", got)
	}
	out.Reset()
	if err := a.Run([]string{"tfx", "state", "mv", "aws_instance.example", "aws_instance.renamed", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Executing terraform state mv") || !strings.Contains(out.String(), "terraform args=state,mv,aws_instance.example,aws_instance.renamed") {
		t.Fatalf("state mv=%q", out.String())
	}
	out.Reset()
	a.in = strings.NewReader("no\n")
	if err := a.Run([]string{"tfx", "state", "rm", "aws_s3_bucket.logs"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Terraform state rm targets") || strings.Contains(out.String(), "terraform args=state,rm") {
		t.Fatalf("cancelled state rm=%q", out.String())
	}
	if err := a.Run([]string{"tfx", "state", "rm", "missing", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing state rm err=%v exit=%d", err, ExitCode(err))
	}
}

func TestTFXCleanUsesExactTargetsAndRejectsSymlinks(t *testing.T) {
	a, out, _ := tfxTestApp(t)
	root := t.TempDir()
	for _, path := range []string{".tf-review/log", "tfplan", "sub/.tf-review/log", ".terraform/cache", ".terraform/modules/example/tfplan"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Run([]string{"tfx", "clean", "--repo", root, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tf-review")); !os.IsNotExist(err) {
		t.Fatalf("review remains: %v output=%q", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "tfplan")); !os.IsNotExist(err) {
		t.Fatalf("plan remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", ".tf-review")); err != nil {
		t.Fatalf("nested scope cleaned unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".terraform")); err != nil {
		t.Fatalf("terraform cleaned without deep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".terraform", "modules", "example", "tfplan")); err != nil {
		t.Fatalf("plan inside .terraform cleaned without deep: %v", err)
	}
	if !strings.Contains(out.String(), "Terraform cleanup targets") {
		t.Fatalf("clean output=%q", out.String())
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Join(root, "sub", ".tf-review"), link); err == nil {
		if err := os.Rename(link, filepath.Join(root, ".tf-review")); err != nil {
			t.Fatal(err)
		}
		if err := a.Run([]string{"tfx", "clean", "--repo", root, "--yes"}); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("symlink clean=%v", err)
		}
	}
}

func TestTFXReviewAllSkipsTerraformModuleRoots(t *testing.T) {
	a, out, state := tfxTestApp(t)
	root := t.TempDir()
	for _, path := range []string{"backend.tf", ".terraform/modules/copied/backend.tf"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("terraform {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Run([]string{"tfx", "review", "--all", "--repo", root}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "=>") != 1 || !strings.Contains(out.String(), root+" => NOCHANGE") {
		t.Fatalf("review output=%q", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".tf-review")); !os.IsNotExist(err) {
		t.Fatalf("review wrote project-local output: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(state, "bb"))
	if err != nil {
		t.Fatal(err)
	}
	var reviewDir string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tfx-review-") {
			reviewDir = filepath.Join(state, "bb", entry.Name())
		}
	}
	if reviewDir == "" {
		t.Fatal("private review directory not created")
	}
	info, err := os.Stat(reviewDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("review dir mode=%v", info.Mode())
	}
	for _, name := range []string{"init.log", "plan.log", "plan.json"} {
		info, err := os.Stat(filepath.Join(reviewDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v", name, info.Mode())
		}
	}
}

func TestTFXCleanRefusesTargetReplacementDuringConfirmation(t *testing.T) {
	a, _, _ := tfxTestApp(t)
	root := t.TempDir()
	plan := filepath.Join(root, "tfplan")
	if err := os.WriteFile(plan, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.in = &replaceOnFirstRead{reader: strings.NewReader("y\n"), source: plan, replacement: []byte("new")}
	err := a.Run([]string{"tfx", "clean", "--repo", root})
	if ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "changed during confirmation") {
		t.Fatalf("err=%v", err)
	}
	contents, readErr := os.ReadFile(plan)
	if readErr != nil || string(contents) != "new" {
		t.Fatalf("replacement changed: %q err=%v", contents, readErr)
	}
}

func TestTFXCleanIgnoresCallerControlledPlanBasename(t *testing.T) {
	a, _, _ := tfxTestApp(t)
	a.env = append(a.env, "TFPLAN_FILE=backend.tf", "TFDESTROY_PLAN_FILE=main.tf")
	root := t.TempDir()
	for _, name := range []string{"backend.tf", "main.tf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Run([]string{"tfx", "clean", "--repo", root, "--yes"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backend.tf", "main.tf"} {
		if contents, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(contents) != "source" {
			t.Fatalf("%s contents=%q err=%v", name, contents, err)
		}
	}
}

func TestClassifyTFXPlanDefaultsAndTypedRules(t *testing.T) {
	update := []byte(`{"resource_changes":[{"address":"aws_instance.x","change":{"actions":["update"],"before":{"tags":{"Name":"old"}},"after":{"tags":{"Name":"new"}}}}]}`)
	status, _, err := classifyTFXPlan(update, defaultTFXReviewRules())
	if err != nil || status != "EXPECTED" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	create := []byte(`{"resource_changes":[{"address":"aws_instance.x","change":{"actions":["create"],"before":null,"after":{"name":"new"}}}]}`)
	status, _, err = classifyTFXPlan(create, defaultTFXReviewRules())
	if err != nil || status != "REVIEW" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	status, _, err = classifyTFXPlan(create, tfxReviewRules{AllowActions: []string{"create"}, AllowPaths: nil, Match: "prefix"})
	if err != nil || status != "EXPECTED" {
		t.Fatalf("configured status=%s err=%v", status, err)
	}
}
