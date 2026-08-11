package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type assumeCredentials struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

func (a *App) assume(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb assume [profile]              Select or apply AWS CLI-resolved credentials
  bb assume list                   List configured profiles
  bb assume current                Show current environment and caller identity
  bb assume unset                  Remove assumed AWS variables from the current shell
  bb assume exec <profile> -- <command> [args...]
  bb assume profile [profile arguments...]

Credentials are resolved by "aws configure export-credentials" and are never
stored or journaled by bb. Run "bb profile login <name>" when SSO login is needed.
`)
		return err
	}
	if len(args) == 0 {
		profile, err := a.chooseAssumeProfile()
		if err != nil || profile == "" {
			return err
		}
		return a.assumeApply(profile)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usage("assume list", "")
		}
		return a.profileList(nil)
	case "current":
		if len(args) != 1 {
			return usage("assume current", "")
		}
		return a.assumeCurrent()
	case "unset":
		if len(args) != 1 {
			return usage("assume unset", "")
		}
		return writeAssumeUnset(a.out)
	case "exec":
		return a.assumeExec(args[1:])
	case "profile":
		if len(args) == 1 {
			return a.profileList(nil)
		}
		return a.profile(args[1:])
	default:
		if len(args) != 1 || !awsProfileNameRE.MatchString(args[0]) {
			return usage("assume", "[profile]")
		}
		return a.assumeApply(args[0])
	}
}

func (a *App) chooseAssumeProfile() (string, error) {
	config, err := a.readAWSConfig()
	if err != nil {
		return "", err
	}
	names := profileNames(config)
	sort.Strings(names)
	choices := make([]selectChoice, len(names))
	for i, name := range names {
		fields := sectionFields(config, profileHeader(name))
		detail := make([]string, 0, 2)
		if region := fields["region"]; region != "" {
			detail = append(detail, region)
		}
		if role := fields["sso_role_name"]; role != "" {
			detail = append(detail, role)
		}
		choices[i] = selectChoice{
			Value:       name,
			Label:       name,
			Description: strings.Join(detail, " • "),
			SearchText:  strings.Join([]string{fields["sso_session"], fields["sso_account_id"]}, " "),
		}
	}
	return a.selectOne("AWS profile", choices)
}

func (a *App) resolveAssumeCredentials(profile string) (assumeCredentials, error) {
	if !awsProfileNameRE.MatchString(profile) {
		return assumeCredentials{}, invalid("invalid AWS profile name")
	}
	if _, err := a.lookPath("aws"); err != nil {
		return assumeCredentials{}, unavailable("aws is not installed; run 'bb doctor' for dependency guidance")
	}
	cmd := a.command("aws", "configure", "export-credentials", "--profile", profile, "--format", "process")
	cmd.Env = a.env
	cmd.Stdin = a.in
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return assumeCredentials{}, fmt.Errorf("resolve AWS credentials for %s: %s; run 'bb profile login %s' if SSO login is required", profile, message, profile)
	}
	var credentials assumeCredentials
	if err := json.Unmarshal(stdout.Bytes(), &credentials); err != nil {
		return assumeCredentials{}, fmt.Errorf("parse AWS credentials for %s: %w", profile, err)
	}
	if credentials.AccessKeyID == "" || credentials.SecretAccessKey == "" {
		return assumeCredentials{}, invalid("AWS CLI returned incomplete credentials for " + profile)
	}
	return credentials, nil
}

func (a *App) assumeRegion(profile string) string {
	config, err := a.readAWSConfig()
	if err != nil {
		return ""
	}
	return sectionFields(config, profileHeader(profile))["region"]
}

func (a *App) assumeApply(profile string) error {
	if output, ok := a.out.(*os.File); ok && isCharacterDevice(output) {
		return invalid("refusing to print credentials to a terminal; load 'bb shell init zsh' or use 'bb assume exec'")
	}
	credentials, err := a.resolveAssumeCredentials(profile)
	if err != nil {
		return err
	}
	return writeAssumeExports(a.out, profile, a.assumeRegion(profile), credentials)
}

func writeAssumeExports(out io.Writer, profile, region string, credentials assumeCredentials) error {
	lines := []string{
		"unset AWS_PROFILE",
		"export AWS_ACCESS_KEY_ID=" + shellQuote(credentials.AccessKeyID),
		"export AWS_SECRET_ACCESS_KEY=" + shellQuote(credentials.SecretAccessKey),
	}
	if credentials.SessionToken == "" {
		lines = append(lines, "unset AWS_SESSION_TOKEN")
	} else {
		lines = append(lines, "export AWS_SESSION_TOKEN="+shellQuote(credentials.SessionToken))
	}
	if credentials.Expiration == "" {
		lines = append(lines, "unset AWS_SESSION_EXPIRATION", "unset AWS_CREDENTIAL_EXPIRATION")
	} else {
		lines = append(lines,
			"export AWS_SESSION_EXPIRATION="+shellQuote(credentials.Expiration),
			"export AWS_CREDENTIAL_EXPIRATION="+shellQuote(credentials.Expiration),
		)
	}
	lines = append(lines, "export BINBOX_ASSUME_PROFILE="+shellQuote(profile))
	if region != "" {
		lines = append(lines, "export AWS_REGION="+shellQuote(region), "export AWS_DEFAULT_REGION="+shellQuote(region))
	}
	_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
	return err
}

func writeAssumeUnset(out io.Writer) error {
	_, err := fmt.Fprint(out, `unset AWS_ACCESS_KEY_ID
unset AWS_SECRET_ACCESS_KEY
unset AWS_SESSION_TOKEN
unset AWS_PROFILE
unset AWS_REGION
unset AWS_DEFAULT_REGION
unset AWS_SESSION_EXPIRATION
unset AWS_CREDENTIAL_EXPIRATION
unset BINBOX_ASSUME_PROFILE
`)
	return err
}

func (a *App) assumeCurrent() error {
	fmt.Fprintf(a.out, "AWS_PROFILE=%s\n", a.getenv("AWS_PROFILE"))
	fmt.Fprintf(a.out, "BINBOX_ASSUME_PROFILE=%s\n", a.getenv("BINBOX_ASSUME_PROFILE"))
	fmt.Fprintf(a.out, "AWS_REGION=%s\n", firstNonEmpty(a.getenv("AWS_REGION"), a.getenv("AWS_DEFAULT_REGION")))
	fmt.Fprintf(a.out, "AWS_SESSION_EXPIRATION=%s\n", firstNonEmpty(a.getenv("AWS_SESSION_EXPIRATION"), a.getenv("AWS_CREDENTIAL_EXPIRATION")))
	return a.runExternal("aws", "sts", "get-caller-identity", "--output", "json")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (a *App) assumeExec(args []string) error {
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator != 1 || len(args) <= 2 || !awsProfileNameRE.MatchString(args[0]) {
		return usage("assume exec", "<profile> -- <command> [args...]")
	}
	profile := args[0]
	credentials, err := a.resolveAssumeCredentials(profile)
	if err != nil {
		return err
	}
	cmd := a.command(args[2], args[3:]...)
	cmd.Env = assumeEnvironment(a.env, profile, a.assumeRegion(profile), credentials)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run with AWS profile %s: %w", profile, err)
	}
	return nil
}

func assumeEnvironment(base []string, profile, region string, credentials assumeCredentials) []string {
	replaced := map[string]bool{
		"AWS_PROFILE": true, "AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true,
		"AWS_SESSION_TOKEN": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
		"AWS_SESSION_EXPIRATION": true, "AWS_CREDENTIAL_EXPIRATION": true,
		"BINBOX_ASSUME_PROFILE": true,
	}
	env := make([]string, 0, len(base)+8)
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !replaced[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"AWS_ACCESS_KEY_ID="+credentials.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+credentials.SecretAccessKey,
		"BINBOX_ASSUME_PROFILE="+profile,
	)
	if credentials.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+credentials.SessionToken)
	}
	if credentials.Expiration != "" {
		env = append(env, "AWS_SESSION_EXPIRATION="+credentials.Expiration, "AWS_CREDENTIAL_EXPIRATION="+credentials.Expiration)
	}
	if region != "" {
		env = append(env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	}
	return env
}
