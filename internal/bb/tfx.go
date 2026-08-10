package bb

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tfx is a deliberately restricted compatibility surface for the former shell
// command. Terraform is always invoked with direct argv (never through a
// shell). The mutation commands are guarded by the legacy, account-bound
// tfsession file so existing users can migrate without weakening that boundary.
func (a *App) tfx(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb tfx init [terraform arguments...]
  bb tfx validate [terraform arguments...]
  bb tfx fmt [terraform arguments...]
  bb tfx plan [terraform arguments...]
  bb tfx sum [summary|tree|stree|draw|md|json] [output] [plan]
  bb tfx session [minutes] [-d|--destroy]
  bb tfx apply [terraform arguments...]
  bb tfx destroy [terraform arguments...]
  bb tfx status [--json]

  bb tfx end
  bb tfx state list [terraform state list arguments...]

The plan command always writes to TFPLAN_FILE (default: tfplan), and refuses a
caller-provided -out flag. apply and destroy require an account-bound session
and an interactive confirmation. review, clean, and state show/mv/rm remain
unimplemented.
`)
		return err
	}
	switch args[0] {
	case "init", "validate", "fmt":
		return a.tfxTerraform(args[0], args[1:]...)
	case "plan":
		return a.tfxPlan(args[1:])
	case "sum":
		return a.tfxSum(args[1:])
	case "status":
		return a.tfxStatus(args[1:])
	case "session":
		return a.tfxSession(args[1:])
	case "apply":
		return a.tfxApply(args[1:])
	case "destroy":
		return a.tfxDestroy(args[1:])
	case "end":
		return a.tfxEnd(args[1:])
	case "state":
		return a.tfxState(args[1:])
	default:
		return invalid(fmt.Sprintf("unsupported tfx command %q; run 'bb tfx help'", args[0]))
	}
}

func (a *App) tfxTerraform(command string, args ...string) error {
	if _, err := a.lookPath("terraform"); err != nil {
		return unavailable("terraform is not installed; install terraform to use bb tfx")
	}
	cmd := a.command("terraform", append([]string{command}, args...)...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = a.env, a.in, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform %s: %w", command, err)
	}
	return nil
}

// tfxTerraformWithoutInput is used while preparing a destructive plan. Keeping
// stdin free guarantees that the later, separate confirmation belongs to bb,
// not to Terraform's variable prompt.
func (a *App) tfxTerraformWithoutInput(command string, args ...string) error {
	if _, err := a.lookPath("terraform"); err != nil {
		return unavailable("terraform is not installed; install terraform to use bb tfx")
	}
	cmd := a.command("terraform", append([]string{command}, args...)...)
	cmd.Env, cmd.Stdout, cmd.Stderr = a.env, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform %s: %w", command, err)
	}
	return nil
}

func (a *App) tfxPlan(args []string) error {
	for _, arg := range args {
		if arg == "-out" || arg == "--out" || strings.HasPrefix(arg, "-out=") || strings.HasPrefix(arg, "--out=") {
			return invalid("bb tfx plan owns Terraform's -out flag; set TFPLAN_FILE instead")
		}
		if arg == "-destroy" || arg == "--destroy" || strings.HasPrefix(arg, "-destroy=") || strings.HasPrefix(arg, "--destroy=") {
			return invalid("bb tfx plan does not accept -destroy; use an account-bound 'bb tfx destroy' session")
		}
	}
	plan := a.getenv("TFPLAN_FILE")
	if plan == "" {
		plan = "tfplan"
	}
	return a.tfxTerraform("plan", append([]string{"-out=" + plan}, args...)...)
}

func (a *App) tfxSum(args []string) error {
	mode := "summary"
	if len(args) > 0 {
		mode, args = args[0], args[1:]
	}
	flags := []string{}
	switch mode {
	case "summary":
	case "tree":
		flags = append(flags, "-tree")
	case "stree":
		flags = append(flags, "-separate-tree")
	case "draw":
		flags = append(flags, "-tree", "-draw")
	case "md":
		flags = append(flags, "-md")
	case "json":
		flags = append(flags, "-json")
	default:
		return invalid(fmt.Sprintf("unknown tfx sum mode %q", mode))
	}
	plan := a.getenv("TFPLAN_FILE")
	if plan == "" {
		plan = "tfplan"
	}
	if mode == "md" {
		switch len(args) {
		case 0:
		case 1:
			if filepath.Base(args[0]) == "tfplan" {
				plan = args[0]
			} else {
				flags = append(flags, "-out="+args[0])
			}
		case 2:
			flags, plan = append(flags, "-out="+args[0]), args[1]
		default:
			return usage("tfx sum md", "[output] [plan]")
		}
	} else {
		if len(args) > 1 {
			return usage("tfx sum", "[summary|tree|stree|draw|md|json] [plan]")
		}
		if len(args) == 1 {
			plan = args[0]
		}
	}
	if _, err := os.Stat(plan); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plan file not found: %s (run 'bb tfx plan' first)", plan)
		}
		return fmt.Errorf("inspect plan file %s: %w", plan, err)
	}
	if _, err := a.lookPath("terraform"); err != nil {
		return unavailable("terraform is not installed; install terraform to use bb tfx sum")
	}
	if _, err := a.lookPath("tf-summarize"); err != nil {
		return unavailable("tf-summarize is not installed; install tf-summarize to use bb tfx sum")
	}
	terraform := a.command("terraform", "show", "-json", plan)
	summarize := a.command("tf-summarize", flags...)
	var terraformStderr, summarizeStderr bytes.Buffer
	terraform.Env, terraform.Stderr = a.env, &terraformStderr
	summarize.Env, summarize.Stdout, summarize.Stderr = a.env, a.out, &summarizeStderr
	pipe, err := terraform.StdoutPipe()
	if err != nil {
		return fmt.Errorf("prepare terraform show output: %w", err)
	}
	summarize.Stdin = pipe
	if err := summarize.Start(); err != nil {
		return fmt.Errorf("start tf-summarize: %w", err)
	}
	if err := terraform.Start(); err != nil {
		_ = pipe.Close()
		_ = summarize.Wait()
		_, _ = a.err.Write(summarizeStderr.Bytes())
		return fmt.Errorf("start terraform show: %w", err)
	}
	terraformErr := terraform.Wait()
	summarizeErr := summarize.Wait()
	_, _ = a.err.Write(terraformStderr.Bytes())
	_, _ = a.err.Write(summarizeStderr.Bytes())
	if terraformErr != nil {
		return fmt.Errorf("terraform show -json %s: %w", plan, terraformErr)
	}
	if summarizeErr != nil {
		return fmt.Errorf("tf-summarize: %w", summarizeErr)
	}
	return nil
}

type tfxSession struct {
	ExpiresAt int64
	Account   string
	Scope     string
}

func (a *App) tfxSessionFile() (string, error) {
	stateHome := a.getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home := a.getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("find home directory for legacy tfx state: %w", err)
			}
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "binbox", "tfsession"), nil
}

func (a *App) readTFXSession() (tfxSession, error) {
	path, err := a.tfxSessionFile()
	if err != nil {
		return tfxSession{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tfxSession{}, unavailable("tfx session is missing; start one with 'bb tfx session'")
		}
		return tfxSession{}, fmt.Errorf("read legacy tfx session: %w", err)
	}
	line := strings.TrimSuffix(string(b), "\n")
	fields := strings.Split(line, "\t")
	if len(fields) != 2 && len(fields) != 3 {
		return tfxSession{}, invalid("legacy tfx session is malformed; run 'bb tfx end' then 'bb tfx session'")
	}
	expires, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || expires <= 0 || fields[1] == "" {
		return tfxSession{}, invalid("legacy tfx session is malformed; run 'bb tfx end' then 'bb tfx session'")
	}
	scope := "apply" // The historical two-column format was apply-only.
	if len(fields) == 3 && fields[2] != "" {
		scope = fields[2]
	}
	if scope != "apply" && scope != "destroy" {
		return tfxSession{}, invalid("legacy tfx session has an unsupported scope; run 'bb tfx end' then 'bb tfx session'")
	}
	return tfxSession{ExpiresAt: expires, Account: fields[1], Scope: scope}, nil
}

func (a *App) requireTFXSession() (tfxSession, error) {
	s, err := a.readTFXSession() // Always re-read immediately before a mutation.
	if err != nil {
		return tfxSession{}, err
	}
	if a.now().Unix() >= s.ExpiresAt {
		// Do not delete expired legacy files: an older binbox installation may
		// still need to inspect it, and only the explicit `end` command mutates it.
		return tfxSession{}, unavailable("tfx session has expired; start a new one with 'bb tfx session'")
	}
	return s, nil
}

func (a *App) awsIdentity() (string, string, error) {
	if _, err := a.lookPath("aws"); err != nil {
		return "", "", unavailable("aws is not installed; install awscli to use guarded bb tfx mutations")
	}
	cmd := a.command("aws", "sts", "get-caller-identity", "--output", "text", "--query", "[Account,Arn]")
	// STS is non-interactive. Do not attach a.in here: with an in-memory or
	// piped confirmation stream that could consume the answer intended for the
	// subsequent guard prompt.
	cmd.Env, cmd.Stderr = a.env, a.err
	b, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("get AWS caller identity: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || fields[0] == "" {
		return "", "", fmt.Errorf("get AWS caller identity: unexpected response")
	}
	return fields[0], fields[1], nil
}

func (a *App) tfxSession(args []string) error {
	minutes, scope := 15, "apply"
	seenMinutes := false
	for _, arg := range args {
		switch arg {
		case "-d", "--destroy":
			scope = "destroy"
		default:
			if strings.HasPrefix(arg, "-") || seenMinutes {
				return usage("tfx session", "[minutes] [-d|--destroy]")
			}
			parsed, err := strconv.Atoi(arg)
			if err != nil || parsed < 1 {
				return invalid("tfx session minutes must be a positive integer")
			}
			minutes, seenMinutes = parsed, true
		}
	}
	account, _, err := a.awsIdentity()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.out, "Starting %s tfx session for account ****%s.\nConfirm account last 4 digits: ", scope, last4(account)); err != nil {
		return err
	}
	input, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && len(input) == 0 {
		return fmt.Errorf("read account confirmation: %w", err)
	}
	if strings.TrimSpace(input) != last4(account) {
		return invalid("account confirmation did not match; tfx session was not created")
	}
	return a.writeTFXSession(tfxSession{ExpiresAt: a.now().Add(time.Duration(minutes) * time.Minute).Unix(), Account: account, Scope: scope})
}

func (a *App) writeTFXSession(s tfxSession) error {
	path, err := a.tfxSessionFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create legacy tfx state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tfsession-*")
	if err != nil {
		return fmt.Errorf("create tfx session atomically: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure tfx session: %w", err)
	}
	if _, err := fmt.Fprintf(tmp, "%d\t%s\t%s\n", s.ExpiresAt, s.Account, s.Scope); err != nil {
		tmp.Close()
		return fmt.Errorf("write tfx session: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync tfx session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tfx session: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace tfx session: %w", err)
	}
	return nil
}

func (a *App) confirmTFX(question string) (bool, error) {
	if _, err := fmt.Fprintf(a.out, "%s [y/N]: ", question); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func (a *App) revalidateTFXMutation(expected tfxSession, requiredScope string) error {
	current, err := a.requireTFXSession()
	if err != nil {
		return err
	}
	if current.Account != expected.Account || current.Scope != expected.Scope || current.ExpiresAt != expected.ExpiresAt {
		return unavailable("tfx session changed during confirmation; review it and start again")
	}
	if requiredScope != "" && current.Scope != requiredScope {
		return unavailable("tfx session scope changed during confirmation; start the required session again")
	}
	account, _, err := a.awsIdentity()
	if err != nil {
		return err
	}
	if account != current.Account {
		return unavailable("current AWS account changed during confirmation; start a new tfx session")
	}
	return nil
}

func (a *App) tfxApply(args []string) error {
	s, err := a.requireTFXSession()
	if err != nil {
		return err
	}
	account, _, err := a.awsIdentity()
	if err != nil {
		return err
	}
	if account != s.Account {
		return unavailable("tfx session account does not match the current AWS account; start a new session")
	}
	plan := a.getenv("TFPLAN_FILE")
	if plan == "" {
		plan = "tfplan"
	}
	if _, err := os.Stat(plan); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plan file not found: %s (run 'bb tfx plan' first)", plan)
		}
		return fmt.Errorf("inspect plan file %s: %w", plan, err)
	}
	ok, err := a.confirmTFX("Apply saved plan " + plan + "?")
	if err != nil {
		return err
	}
	if !ok {
		_, err = fmt.Fprintln(a.out, "Cancelled.")
		return err
	}
	if err := a.revalidateTFXMutation(s, ""); err != nil {
		return err
	}
	return a.tfxTerraform("apply", append(args, plan)...)
}

func (a *App) tfxDestroy(args []string) error {
	for _, arg := range args {
		if arg == "-auto-approve" || arg == "--auto-approve" || strings.HasPrefix(arg, "-auto-approve=") || strings.HasPrefix(arg, "--auto-approve=") {
			return invalid("bb tfx destroy does not allow -auto-approve")
		}
		if arg == "-out" || arg == "--out" || strings.HasPrefix(arg, "-out=") || strings.HasPrefix(arg, "--out=") {
			return invalid("bb tfx destroy owns Terraform's -out flag; set TFDESTROY_PLAN_FILE instead")
		}
		if arg == "-destroy" || arg == "--destroy" || strings.HasPrefix(arg, "-destroy=") || strings.HasPrefix(arg, "--destroy=") {
			return invalid("bb tfx destroy owns Terraform's -destroy flag")
		}
	}
	s, err := a.requireTFXSession()
	if err != nil {
		return err
	}
	if s.Scope != "destroy" {
		return unavailable("current tfx session is apply-only; start a destroy session with 'bb tfx session -d'")
	}
	account, _, err := a.awsIdentity()
	if err != nil {
		return err
	}
	if account != s.Account {
		return unavailable("tfx session account does not match the current AWS account; start a new session")
	}
	plan := a.getenv("TFDESTROY_PLAN_FILE")
	if plan == "" {
		plan = "tfdestroyplan"
	}
	if err := a.tfxTerraformWithoutInput("plan", append([]string{"-destroy", "-out=" + plan}, args...)...); err != nil {
		return err
	}
	if _, err := os.Stat(plan); err != nil {
		return fmt.Errorf("destroy plan was not created at %s: %w", plan, err)
	}
	ok, err := a.confirmTFX("Apply destroy plan " + plan + "?")
	if err != nil {
		return err
	}
	if !ok {
		_, err = fmt.Fprintln(a.out, "Cancelled.")
		return err
	}
	if err := a.revalidateTFXMutation(s, "destroy"); err != nil {
		return err
	}
	return a.tfxTerraform("apply", plan)
}

func (a *App) tfxEnd(args []string) error {
	if len(args) != 0 {
		return usage("tfx end", "")
	}
	path, err := a.tfxSessionFile()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("end tfx session: %w", err)
	}
	_, err = fmt.Fprintln(a.out, "tfx session ended")
	return err
}

type tfxSessionStatus struct {
	SessionFile  string `json:"session_file"`
	State        string `json:"state"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	AccountLast4 string `json:"account_last4,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tfxStatus reads the legacy tfsession TSV without changing it. Legacy files
// contain expiry, account, and optionally scope; account is never emitted in
// full, including JSON output.
func (a *App) tfxStatus(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 0 {
		return usage("tfx status", "[--json]")
	}
	stateHome := a.getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home := a.getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find home directory for legacy tfx state: %w", err)
			}
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	// Preserve the legacy path exactly. New bb-owned state lives under `bb`,
	// while tfx historically used `binbox`; status is read-only compatibility.
	result := tfxSessionStatus{SessionFile: filepath.Join(stateHome, "binbox", "tfsession"), State: "missing"}
	bytes, readErr := os.ReadFile(result.SessionFile)
	if readErr == nil {
		fields := strings.Split(strings.TrimSpace(string(bytes)), "\t")
		if len(fields) != 2 && len(fields) != 3 {
			result.State = "malformed"
		} else if expiry, parseErr := strconv.ParseInt(fields[0], 10, 64); parseErr != nil {
			result.State = "malformed"
		} else {
			result.ExpiresAt = timeFromUnix(expiry)
			result.AccountLast4 = last4(fields[1])
			result.Scope = "apply"
			if len(fields) == 3 && fields[2] != "" {
				result.Scope = fields[2]
			}
			if a.now().Unix() >= expiry {
				result.State = "expired"
			} else {
				result.State = "valid"
			}
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read legacy tfx session: %w", readErr)
	}
	if jsonMode {
		return printEnvelope(a.out, result, nil)
	}
	if result.State == "valid" {
		_, err := fmt.Fprintf(a.out, "tfx session valid (account: ****%s, scope: %s, expires: %s)\n", result.AccountLast4, result.Scope, result.ExpiresAt)
		return err
	} else {
		if _, err := fmt.Fprintf(a.out, "tfx session %s\n", result.State); err != nil {
			return err
		}
		return &CommandError{Code: "operational_error", Message: "tfx session " + result.State, Exit: ExitOperational, Reported: true}
	}
}

func timeFromUnix(value int64) string { return time.Unix(value, 0).UTC().Format(time.RFC3339) }

func last4(value string) string {
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}

func (a *App) tfxState(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return invalid("only 'bb tfx state list' is available in the safe tranche")
	}
	return a.tfxTerraform("state", append([]string{"list"}, args[1:]...)...)
}
