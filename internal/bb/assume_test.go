package bb

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssumeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ASSUME_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	name, args := os.Args[separator], os.Args[separator+1:]
	if name == "aws" && len(args) >= 2 && args[0] == "configure" && args[1] == "export-credentials" {
		profile := ""
		for i, arg := range args {
			if arg == "--profile" && i+1 < len(args) {
				profile = args[i+1]
				break
			}
		}
		switch profile {
		case "expired":
			fmt.Fprint(os.Stderr, "The SSO session associated with this profile has expired")
			os.Exit(1)
		case "incomplete":
			fmt.Fprint(os.Stdout, `{"Version":1,"AccessKeyId":"AKIA_TEST"}`)
			os.Exit(0)
		}
		fmt.Fprint(os.Stdout, `{"Version":1,"AccessKeyId":"AKIA_TEST","SecretAccessKey":"secret-test","SessionToken":"token-test","Expiration":"2026-08-11T12:00:00Z"}`)
		os.Exit(0)
	}
	if name == "aws" && len(args) >= 2 && args[0] == "sts" && args[1] == "get-caller-identity" {
		fmt.Fprint(os.Stdout, `{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/test"}`)
		os.Exit(0)
	}
	if name == "aws" && len(args) == 4 && args[0] == "sso" && args[1] == "login" && args[2] == "--sso-session" {
		fmt.Fprint(os.Stdout, "logged-in:"+args[3])
		os.Exit(0)
	}
	if name == "envcheck" {
		fmt.Fprintf(os.Stdout, "%s|%s|%s|%s", os.Getenv("BINBOX_ASSUME_PROFILE"), os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_REGION"), os.Getenv("AWS_PROFILE"))
		os.Exit(0)
	}
	os.Exit(90)
}

func assumeTestApp(t *testing.T, input string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, ".aws", "config")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "[sso-session corp]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-northeast-2\n\n[profile alpha]\nsso_session = corp\nregion = us-east-1\n\n[profile prod]\nsso_session = corp\nregion = ap-northeast-2\n"
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GO_WANT_ASSUME_HELPER=1", "AWS_PROFILE=stale"}
	a := New(stdout, stderr, env)
	a.in = strings.NewReader(input)
	a.lookPath = func(name string) (string, error) {
		if name == "aws" {
			return "helper", nil
		}
		return exec.LookPath(name)
	}
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestAssumeHelperProcess", "--", name}, args...)...)
	}
	return a, stdout, stderr
}

func TestAssumeSelectsProfileAndEmitsShellExports(t *testing.T) {
	a, out, _ := assumeTestApp(t, "2\n")
	if err := a.Run([]string{"assume"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"unset AWS_PROFILE\n",
		"export AWS_ACCESS_KEY_ID='AKIA_TEST'\n",
		"export AWS_SECRET_ACCESS_KEY='secret-test'\n",
		"export AWS_SESSION_TOKEN='token-test'\n",
		"export AWS_CREDENTIAL_EXPIRATION='2026-08-11T12:00:00Z'\n",
		"export BINBOX_ASSUME_PROFILE='prod'\n",
		"export AWS_REGION='ap-northeast-2'\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("exports missing %q: %s", want, out.String())
		}
	}
}

func TestAWSSSOSelectsSessionAndAssumeUsesProfile(t *testing.T) {
	a, out, _ := assumeTestApp(t, "1\n")
	if err := a.Run([]string{"aws", "sso"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "logged-in:corp"; got != want {
		t.Fatalf("sso login=%q, want %q", got, want)
	}

	out.Reset()
	if err := a.Run([]string{"aws", "assume", "prod"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "export BINBOX_ASSUME_PROFILE='prod'") || !strings.Contains(out.String(), "export AWS_REGION='ap-northeast-2'") {
		t.Fatalf("aws assume exports=%q", out.String())
	}
}

func TestAWSSSOListsSessionsAndRejectsMissingSession(t *testing.T) {
	a, out, _ := assumeTestApp(t, "")
	if err := a.Run([]string{"aws", "sso", "list"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "corp\n"; got != want {
		t.Fatalf("sessions=%q, want %q", got, want)
	}
	out.Reset()
	if err := a.Run([]string{"aws", "sso", "missing"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing session err=%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("missing session wrote stdout: %q", out.String())
	}
}

func TestAssumeUnsetAndExecAreScoped(t *testing.T) {
	a, out, _ := assumeTestApp(t, "")
	if err := a.Run([]string{"assume", "unset"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unset AWS_ACCESS_KEY_ID\n") || !strings.Contains(out.String(), "unset BINBOX_ASSUME_PROFILE\n") {
		t.Fatalf("unset=%q", out.String())
	}

	out.Reset()
	if err := a.Run([]string{"assume", "exec", "prod", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "prod|AKIA_TEST|ap-northeast-2|"; got != want {
		t.Fatalf("exec environment=%q, want %q", got, want)
	}
}

func TestAssumeCurrentAndProfileCompatibility(t *testing.T) {
	a, out, _ := assumeTestApp(t, "")
	if err := a.Run([]string{"assume", "current"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "AWS_PROFILE=stale") || !strings.Contains(out.String(), `"Account":"123456789012"`) {
		t.Fatalf("current=%q", out.String())
	}
	out.Reset()
	if err := a.Run([]string{"assume", "profile"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "alpha\nprod\n"; got != want {
		t.Fatalf("profiles=%q, want %q", got, want)
	}
}

func TestAssumeCredentialFailuresEmitNoShellOutput(t *testing.T) {
	for _, test := range []struct {
		profile string
		want    string
	}{
		{profile: "expired", want: "bb aws sso"},
		{profile: "incomplete", want: "incomplete credentials"},
	} {
		t.Run(test.profile, func(t *testing.T) {
			a, out, _ := assumeTestApp(t, "")
			err := a.Run([]string{"assume", test.profile})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want message containing %q", err, test.want)
			}
			if out.Len() != 0 {
				t.Fatalf("failure emitted eval output: %q", out.String())
			}
		})
	}
}
