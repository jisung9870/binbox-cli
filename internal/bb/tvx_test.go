package bb

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func tvxTestApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, _, _ := testApp(t)
	errOut := new(bytes.Buffer)
	a.err = errOut
	a.env = append(a.env, "GO_WANT_BB_TVX_HELPER=1")
	a.lookPath = func(string) (string, error) { return "trivy", nil }
	a.command = func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestTVXHelperProcess", "--", name}, args...)...)
		cmd.Env = a.env
		return cmd
	}
	return a, out, errOut
}

func TestTVXHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BB_TVX_HELPER") != "1" {
		return
	}
	start := 0
	for i, arg := range os.Args {
		if arg == "--" {
			start = i + 1
			break
		}
	}
	if start == 0 || start >= len(os.Args) {
		os.Exit(92)
	}
	if os.Getenv("TVX_HELPER_EXIT") != "" {
		os.Exit(7)
	}
	fmt.Fprint(os.Stdout, strings.Join(os.Args[start:], "\n"))
	os.Exit(0)
}

func TestTVXDirectArgvCompatibility(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"image", []string{"tvx", "image", "nginx:latest", "--ignore-unfixed"}, "trivy\nimage\n--scanners\nvuln,misconfig,secret\nnginx:latest\n--ignore-unfixed"},
		{"repo default", []string{"tvx", "repo"}, "trivy\nrepo\n--scanners\nvuln,misconfig,secret\n."},
		{"config default", []string{"tvx", "config"}, "trivy\nconfig\n."},
		{"sbom", []string{"tvx", "sbom", "image", "app:v1", "-o", "sbom.json"}, "trivy\nimage\n--format\ncyclonedx\napp:v1\n-o\nsbom.json"},
		{"report", []string{"tvx", "report", "sarif", "repo", ".", "-o", "report.sarif"}, "trivy\nrepo\n--scanners\nvuln,misconfig,secret\n--format\nsarif\n.\n-o\nreport.sarif"},
		{"clean", []string{"tvx", "clean"}, "trivy\nclean\n--scan-cache"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, out, _ := tvxTestApp(t)
			if err := a.Run(tt.args); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tt.want {
				t.Fatalf("argv:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestTVXCIAndPolicyBoundaries(t *testing.T) {
	a, out, _ := tvxTestApp(t)
	if err := a.Run([]string{"tvx", "ci", "repo", "."}); err != nil {
		t.Fatal(err)
	}
	want := "trivy\nrepo\n--scanners\nvuln,misconfig,secret\n--severity\nHIGH,CRITICAL\n--exit-code\n1\n."
	if got := out.String(); got != want {
		t.Fatalf("ci argv=%q want=%q", got, want)
	}
	for _, args := range [][]string{{"tvx", "ci", "repo", ".", "--severity", "LOW"}, {"tvx", "ci", "repo", ".", "--exit-code=0"}, {"tvx", "ci", "repo", ".", "--scanners", "vuln"}} {
		if err := a.Run(args); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%v: %v", args, err)
		}
	}
	a, out, _ = tvxTestApp(t)
	a.env = append(a.env, "TVX_CI_SEVERITY=CRITICAL")
	if err := a.Run([]string{"tvx", "ci", "config", "infra"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "trivy\nconfig\n--severity\nCRITICAL\n--exit-code\n1\ninfra" {
		t.Fatalf("severity argv=%q", got)
	}
	a, _, _ = tvxTestApp(t)
	a.env = append(a.env, "TVX_CI_SEVERITY=high")
	if err := a.Run([]string{"tvx", "ci", "repo"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("invalid severity=%v", err)
	}
}

func TestTVXFormatK8sAndDoctor(t *testing.T) {
	a, out, errOut := tvxTestApp(t)
	if err := a.Run([]string{"tvx", "k8s", "prod", "--skip-images"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "trivy\nkubernetes\n--report\nsummary\n--disable-node-collector\n--skip-images\nprod" {
		t.Fatalf("k8s=%q", got)
	}
	a, out, errOut = tvxTestApp(t)
	a.in = strings.NewReader("yes\n")
	if err := a.Run([]string{"tvx", "k8s", "prod", "--with-node-collector", "--skip-images"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "trivy\nkubernetes\n--report\nsummary\n--skip-images\nprod" {
		t.Fatalf("collector=%q", got)
	}
	if !strings.Contains(errOut.String(), "node collector") {
		t.Fatalf("missing k8s safety warning: %q", errOut.String())
	}
	a, out, _ = tvxTestApp(t)
	if err := a.Run([]string{"tvx", "doctor"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tvx doctor\ntrivy\nversion") || !strings.Contains(out.String(), "contents not read") {
		t.Fatalf("doctor=%q", out.String())
	}
	for _, args := range [][]string{{"tvx", "sbom", "repo", ".", "--format", "json"}, {"tvx", "report", "json", "repo", ".", "-f", "table"}} {
		if err := a.Run(args); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestTVXUnavailableAndExitCode(t *testing.T) {
	a, _, _ := tvxTestApp(t)
	a.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := a.Run([]string{"tvx", "repo"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("missing trivy: %v", err)
	}
	a, _, _ = tvxTestApp(t)
	a.env = append(a.env, "TVX_HELPER_EXIT=1")
	if err := a.Run([]string{"tvx", "image", "broken"}); ExitCode(err) != 7 {
		t.Fatalf("exit propagation: %v (%d)", err, ExitCode(err))
	}
}
