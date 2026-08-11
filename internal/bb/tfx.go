package bb

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
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
  bb tfx browse [plan] [--json]
  bb tfx session [minutes] [-d|--destroy]
  bb tfx apply [terraform arguments...]
  bb tfx destroy [terraform arguments...]
  bb tfx status [--json]

  bb tfx end
  bb tfx state list [terraform state list arguments...]
  bb tfx state show [address]
  bb tfx state mv <source> <destination> [--yes]
  bb tfx state rm <address...> [--yes]
  bb tfx review [--all] [--repo path] [root ...]
  bb tfx clean [--all|-r] [--deep] [--repo path] [root ...] [--yes]

The plan command always writes to TFPLAN_FILE (default: tfplan), and refuses a
caller-provided -out flag. apply and destroy require an account-bound session
and an interactive confirmation. Review and clean intentionally accept explicit
roots (or --all) only; they never invoke an interactive selector.

Browse reads an existing plan and never mutates anything. Sensitive and
not-yet-known values are replaced with placeholders rather than printed.
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
	case "browse":
		return a.tfxBrowse(args[1:])
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
	case "review":
		return a.tfxReview(args[1:])
	case "clean":
		return a.tfxClean(args[1:])
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

func (a *App) confirmTFX(question string) (bool, error) { return a.confirmAction(question) }

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

// tfxPlanSnapshot is a private, owner-only copy of a Terraform plan. Terraform
// receives only Path; Source is retained solely for the human confirmation.
// Keeping the original descriptor open while copying makes a rename or replace
// of Source after opening irrelevant to the bytes Terraform later applies.
type tfxPlanSnapshot struct {
	Source string
	Path   string
	SHA256 string
	dir    string
}

func (s tfxPlanSnapshot) cleanup() error {
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove private Terraform plan snapshot: %w", err)
	}
	if err := os.Remove(s.dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove private Terraform plan directory: %w", err)
	}
	return nil
}

// snapshotTFXPlan opens the user-selected source without following symlinks,
// verifies that the opened object is regular, then copies its already-open
// descriptor to a bb-owned state directory. It deliberately never reopens the
// source by pathname.
func (a *App) snapshotTFXPlan(source string) (tfxPlanSnapshot, error) {
	fd, err := syscall.Open(source, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return tfxPlanSnapshot{}, invalid(fmt.Sprintf("Terraform plan source must not be a symlink: %s", source))
		}
		if os.IsNotExist(err) {
			return tfxPlanSnapshot{}, fmt.Errorf("plan file not found: %s (run 'bb tfx plan' first)", source)
		}
		return tfxPlanSnapshot{}, fmt.Errorf("open Terraform plan source %s: %w", source, err)
	}
	sourceFile := os.NewFile(uintptr(fd), source)
	defer sourceFile.Close()
	info, err := sourceFile.Stat()
	if err != nil {
		return tfxPlanSnapshot{}, fmt.Errorf("inspect Terraform plan source %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return tfxPlanSnapshot{}, invalid(fmt.Sprintf("Terraform plan source must be a regular file: %s", source))
	}
	_, state, err := a.paths()
	if err != nil {
		return tfxPlanSnapshot{}, err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return tfxPlanSnapshot{}, fmt.Errorf("create bb state directory for Terraform plan: %w", err)
	}
	dir, err := os.MkdirTemp(state, "tfx-plan-")
	if err != nil {
		return tfxPlanSnapshot{}, fmt.Errorf("create private Terraform plan directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.Remove(dir)
		return tfxPlanSnapshot{}, fmt.Errorf("secure private Terraform plan directory: %w", err)
	}
	path := filepath.Join(dir, "plan")
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(dir)
		return tfxPlanSnapshot{}, fmt.Errorf("create private Terraform plan snapshot: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(destination, hash), sourceFile); err != nil {
		_ = destination.Close()
		_ = os.Remove(path)
		_ = os.Remove(dir)
		return tfxPlanSnapshot{}, fmt.Errorf("copy Terraform plan into private snapshot: %w", err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = os.Remove(path)
		_ = os.Remove(dir)
		return tfxPlanSnapshot{}, fmt.Errorf("sync private Terraform plan snapshot: %w", err)
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(dir)
		return tfxPlanSnapshot{}, fmt.Errorf("close private Terraform plan snapshot: %w", err)
	}
	return tfxPlanSnapshot{Source: source, Path: path, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), dir: dir}, nil
}

func (a *App) tfxApply(args []string) (err error) {
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
	snapshot, err := a.snapshotTFXPlan(plan)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := snapshot.cleanup(); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}()
	if _, err := fmt.Fprintf(a.err, "Saved plan: %s\nSHA-256: %s\n", safeTerminalText(snapshot.Source), snapshot.SHA256); err != nil {
		return err
	}
	ok, err := a.confirmTFX("Apply this saved plan?")
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
	return a.tfxTerraform("apply", append(args, snapshot.Path)...)
}

func (a *App) tfxDestroy(args []string) (err error) {
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
	snapshot, err := a.snapshotTFXPlan(plan)
	if err != nil {
		return fmt.Errorf("snapshot destroy plan %s: %w", plan, err)
	}
	defer func() {
		if cleanupErr := snapshot.cleanup(); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}()
	if _, err := fmt.Fprintf(a.err, "Destroy plan: %s\nSHA-256: %s\n", safeTerminalText(snapshot.Source), snapshot.SHA256); err != nil {
		return err
	}
	ok, err := a.confirmTFX("Apply this destroy plan?")
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
	return a.tfxTerraform("apply", snapshot.Path)
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
	if len(args) == 0 {
		return usage("tfx state", "list|show|mv|rm [arguments]")
	}
	switch args[0] {
	case "list":
		return a.tfxTerraform("state", append([]string{"list"}, args[1:]...)...)
	case "show":
		if len(args) != 2 || !validTFXAddress(args[1]) {
			return usage("tfx state show", "<address>")
		}
		return a.tfxTerraform("state", "show", args[1])
	case "mv":
		args, yes := takeFlag(args[1:], "--yes")
		if len(args) != 2 {
			return usage("tfx state mv", "<source> <destination> [--yes]")
		}
		return a.tfxStateMutate("mv", args, yes)
	case "rm":
		args, yes := takeFlag(args[1:], "--yes")
		if len(args) == 0 {
			return usage("tfx state rm", "<address...> [--yes]")
		}
		return a.tfxStateMutate("rm", args, yes)
	default:
		return invalid("unknown tfx state command; use list, show, mv, or rm")
	}
}

// tfxStateAddresses obtains Terraform's current view immediately before a
// mutation.  Addresses are treated as opaque argv values, never shell text.
func (a *App) tfxStateAddresses() (map[string]bool, error) {
	if _, err := a.lookPath("terraform"); err != nil {
		return nil, unavailable("terraform is not installed; install terraform to use bb tfx state")
	}
	cmd := a.command("terraform", "state", "list")
	cmd.Env, cmd.Stderr = a.env, a.err
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terraform state list: %w", err)
	}
	result := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result[line] = true
		}
	}
	return result, nil
}

func (a *App) tfxStateMutate(operation string, addresses []string, yes bool) error {
	for _, address := range addresses {
		if !validTFXAddress(address) {
			return invalid("Terraform state addresses must be explicit non-option values")
		}
	}
	current, err := a.tfxStateAddresses()
	if err != nil {
		return err
	}
	// For mv only the source is an existing address. rm requires every target.
	selected := addresses
	if operation == "mv" {
		selected = addresses[:1]
	}
	for _, address := range selected {
		if !current[address] {
			return invalid(fmt.Sprintf("Terraform state address is not present: %s", address))
		}
	}
	if operation == "mv" && current[addresses[1]] {
		return invalid(fmt.Sprintf("Terraform state destination already exists: %s", addresses[1]))
	}
	// Re-observe exact selected addresses after the user has had a chance to
	// inspect the operation, preventing a stale list from authorising a change.
	if !yes {
		if _, err := fmt.Fprintf(a.out, "Terraform state %s targets:\n", operation); err != nil {
			return err
		}
		for _, address := range addresses {
			if _, err := fmt.Fprintf(a.out, "  %s\n", address); err != nil {
				return err
			}
		}
		ok, err := a.confirmTFX("Proceed with this Terraform state mutation?")
		if err != nil {
			return err
		}
		if !ok {
			_, err = fmt.Fprintln(a.out, "Cancelled.")
			return err
		}
	}
	fresh, err := a.tfxStateAddresses()
	if err != nil {
		return err
	}
	for _, address := range selected {
		if !fresh[address] {
			return unavailable(fmt.Sprintf("Terraform state changed; selected address is no longer present: %s", address))
		}
	}
	if operation == "mv" && fresh[addresses[1]] {
		return unavailable(fmt.Sprintf("Terraform state changed; destination is now present: %s", addresses[1]))
	}
	if _, err := fmt.Fprintf(a.out, "Executing terraform state %s for:\n", operation); err != nil {
		return err
	}
	for _, address := range addresses {
		if _, err := fmt.Fprintf(a.out, "  %s\n", address); err != nil {
			return err
		}
	}
	return a.tfxTerraform("state", append([]string{operation}, addresses...)...)
}

func validTFXAddress(address string) bool {
	return address != "" && !strings.HasPrefix(address, "-") && !strings.ContainsAny(address, "\x00\r\n")
}

type tfxReviewRules struct {
	AllowPaths   []string `json:"allow_paths"`
	AllowActions []string `json:"allow_actions"`
	Match        string   `json:"match"`
}

func defaultTFXReviewRules() tfxReviewRules {
	return tfxReviewRules{AllowPaths: []string{"tags", "tags_all", "tag", "tag_specifications"}, AllowActions: []string{"update"}, Match: "prefix"}
}

func readTFXReviewRules(root string) (tfxReviewRules, error) {
	rules := defaultTFXReviewRules()
	path := filepath.Join(root, ".tf-review.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rules, nil
	}
	if err != nil {
		return rules, fmt.Errorf("read review rules %s: %w", path, err)
	}
	var configured tfxReviewRules
	if err := json.Unmarshal(b, &configured); err != nil {
		return rules, invalid(fmt.Sprintf("parse review rules %s: %v", path, err))
	}
	if configured.AllowPaths != nil {
		rules.AllowPaths = configured.AllowPaths
	}
	if configured.AllowActions != nil {
		rules.AllowActions = configured.AllowActions
	}
	if configured.Match != "" {
		rules.Match = configured.Match
	}
	if rules.Match != "prefix" && rules.Match != "glob" {
		return rules, invalid("review rules match must be prefix or glob")
	}
	return rules, nil
}

func pathAllowed(path string, rules tfxReviewRules) bool {
	for _, allowed := range rules.AllowPaths {
		if rules.Match == "glob" {
			parts, want := strings.Split(path, "."), strings.Split(allowed, ".")
			if len(parts) < len(want) {
				continue
			}
			good := true
			for i := range want {
				if want[i] != "*" && want[i] != parts[i] {
					good = false
					break
				}
			}
			if good {
				return true
			}
		} else if path == allowed || strings.HasPrefix(path, allowed+".") {
			return true
		}
	}
	return false
}

func scalarPaths(v any, prefix string, into *[]string) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			scalarPaths(x[k], p, into)
		}
	case []any:
		for i, item := range x {
			p := strconv.Itoa(i)
			if prefix != "" {
				p = prefix + "." + p
			}
			scalarPaths(item, p, into)
		}
	default:
		if prefix != "" {
			*into = append(*into, prefix)
		}
	}
}

func valueAtPath(v any, dotted string) any {
	cur := v
	for _, part := range strings.Split(dotted, ".") {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			cur = x[i]
		default:
			return nil
		}
	}
	return cur
}

// tfxResourceChange is one entry of `terraform show -json`'s resource_changes.
// The parallel *_sensitive and after_unknown structures mirror the shape of
// before/after and carry true where a value must not be printed or is not known
// until apply.
type tfxResourceChange struct {
	Address string `json:"address"`
	Change  struct {
		Actions         []string `json:"actions"`
		Before          any      `json:"before"`
		After           any      `json:"after"`
		BeforeSensitive any      `json:"before_sensitive"`
		AfterSensitive  any      `json:"after_sensitive"`
		AfterUnknown    any      `json:"after_unknown"`
	} `json:"change"`
}

func parseTFXPlanChanges(data []byte) ([]tfxResourceChange, error) {
	var plan struct {
		ResourceChanges []tfxResourceChange `json:"resource_changes"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse terraform plan JSON: %w", err)
	}
	return plan.ResourceChanges, nil
}

// tfxUnchanged reports the entries a plan carries without changing anything.
func tfxUnchanged(actions []string) bool {
	return len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read")
}

// tfxChangedPaths lists the attribute paths whose value differs between the
// before and after states, in a stable order.
func tfxChangedPaths(resource tfxResourceChange) []string {
	var paths []string
	scalarPaths(resource.Change.Before, "", &paths)
	scalarPaths(resource.Change.After, "", &paths)
	seen := map[string]bool{}
	var differences []string
	for _, path := range paths {
		if !seen[path] && fmt.Sprintf("%#v", valueAtPath(resource.Change.Before, path)) != fmt.Sprintf("%#v", valueAtPath(resource.Change.After, path)) {
			seen[path] = true
			differences = append(differences, path)
		}
	}
	return differences
}

// tfxNeedsReview applies the review rules to one resource change. An action the
// rules do not allow is always reported; a pure update is reported when it
// touches a path outside the allow list.
func tfxNeedsReview(resource tfxResourceChange, differences []string, rules tfxReviewRules) bool {
	allowedActions := map[string]bool{}
	for _, action := range rules.AllowActions {
		allowedActions[action] = true
	}
	for _, action := range resource.Change.Actions {
		if !allowedActions[action] {
			return true
		}
	}
	if len(resource.Change.Actions) == 1 && resource.Change.Actions[0] == "update" {
		for _, path := range differences {
			if !pathAllowed(path, rules) {
				return true
			}
		}
	}
	return false
}

func classifyTFXPlan(data []byte, rules tfxReviewRules) (string, string, error) {
	changes, err := parseTFXPlanChanges(data)
	if err != nil {
		return "", "", err
	}
	var reviews []string
	changed := false
	for _, resource := range changes {
		if tfxUnchanged(resource.Change.Actions) {
			continue
		}
		changed = true
		differences := tfxChangedPaths(resource)
		if tfxNeedsReview(resource, differences, rules) {
			reviews = append(reviews, fmt.Sprintf("%s [%s] paths=%s", resource.Address, strings.Join(resource.Change.Actions, ","), strings.Join(differences, ",")))
		}
	}
	if !changed {
		return "NOCHANGE", "", nil
	}
	if len(reviews) == 0 {
		return "EXPECTED", "", nil
	}
	return "REVIEW", strings.Join(reviews, "; "), nil
}

func canonicalTFXBase(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", invalid(fmt.Sprintf("repository path is not a directory: %s", path))
	}
	return resolved, nil
}

func tfxContainedPath(base, candidate string) (string, error) {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", candidate, err)
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", invalid(fmt.Sprintf("path escapes repository base: %s", candidate))
	}
	return resolved, nil
}

func (a *App) tfxReview(args []string) error {
	base := a.getenv("TF_REPO")
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	all := false
	var roots []string
	for len(args) > 0 {
		switch args[0] {
		case "--all":
			all = true
			args = args[1:]
		case "--repo":
			if len(args) < 2 {
				return usage("tfx review", "[--all] [--repo path] [root ...]")
			}
			base, args = args[1], args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				return usage("tfx review", "[--all] [--repo path] [root ...]")
			}
			roots = append(roots, args[0])
			args = args[1:]
		}
	}
	if len(roots) == 0 && !all {
		return invalid("tfx review requires explicit roots or --all (interactive selection is not supported)")
	}
	canonical, err := canonicalTFXBase(base)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		err = filepath.WalkDir(canonical, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() && entry.Name() == ".terraform" {
				return filepath.SkipDir
			}
			if !entry.IsDir() && entry.Name() == "backend.tf" {
				roots = append(roots, filepath.Dir(path))
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("discover Terraform roots: %w", err)
		}
	} else {
		resolved := make([]string, 0, len(roots))
		for _, root := range roots {
			p, err := tfxContainedPath(canonical, filepath.Join(canonical, root))
			if err != nil {
				return err
			}
			resolved = append(resolved, p)
		}
		roots = resolved
	}
	if len(roots) == 0 {
		return invalid("no Terraform roots containing backend.tf were found")
	}
	sort.Strings(roots)
	if _, err := a.lookPath("terraform"); err != nil {
		return unavailable("terraform is not installed; install terraform to use bb tfx review")
	}
	needAttention := false
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, "backend.tf")); err != nil {
			return invalid(fmt.Sprintf("backend.tf is required for review: %s", root))
		}
		_, state, err := a.paths()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(state, 0o700); err != nil {
			return fmt.Errorf("create bb state directory: %w", err)
		}
		if err := os.Chmod(state, 0o700); err != nil {
			return fmt.Errorf("secure bb state directory: %w", err)
		}
		outdir, err := os.MkdirTemp(state, "tfx-review-")
		if err != nil {
			return fmt.Errorf("create private review output directory: %w", err)
		}
		if err := os.Chmod(outdir, 0o700); err != nil {
			return fmt.Errorf("secure review output directory: %w", err)
		}
		if err := a.tfxReviewRun(root, outdir, "init", []string{"-input=false", "-reconfigure"}, "init.log", false); err != nil {
			fmt.Fprintf(a.out, "%s => ERROR (init; log: %s)\n", root, filepath.Join(outdir, "init.log"))
			needAttention = true
			continue
		}
		planPath := filepath.Join(outdir, "plan.bin")
		planErr := a.tfxReviewRun(root, outdir, "plan", []string{"-input=false", "-lock=false", "-detailed-exitcode", "-out=" + planPath}, "plan.log", true)
		if planErr != nil {
			fmt.Fprintf(a.out, "%s => ERROR (plan; log: %s)\n", root, filepath.Join(outdir, "plan.log"))
			needAttention = true
			continue
		}
		if err := a.tfxReviewRun(root, outdir, "show", []string{"-json", planPath}, "plan.json", false); err != nil {
			fmt.Fprintf(a.out, "%s => ERROR (show; log: %s)\n", root, filepath.Join(outdir, "plan.log"))
			needAttention = true
			continue
		}
		data, err := os.ReadFile(filepath.Join(outdir, "plan.json"))
		if err != nil {
			return err
		}
		rules, err := readTFXReviewRules(root)
		if err != nil {
			return err
		}
		status, detail, err := classifyTFXPlan(data, rules)
		if err != nil {
			fmt.Fprintf(a.out, "%s => ERROR (classify: %v)\n", root, err)
			needAttention = true
			continue
		}
		fmt.Fprintf(a.out, "%s => %s", root, status)
		if detail != "" {
			fmt.Fprintf(a.out, " %s", detail)
		}
		fmt.Fprintln(a.out)
		if status == "REVIEW" {
			needAttention = true
		}
	}
	if needAttention {
		return &CommandError{Code: "review_required", Message: "Terraform review requires attention", Exit: ExitInvalidInvocation, Reported: true}
	}
	return nil
}

func (a *App) tfxReviewRun(root, outdir, command string, args []string, output string, detailed bool) error {
	cmd := a.command("terraform", append([]string{"-chdir=" + root, command}, args...)...)
	cmd.Env = a.env
	privateRoot, err := os.OpenRoot(outdir)
	if err != nil {
		return fmt.Errorf("open private review output directory: %w", err)
	}
	defer privateRoot.Close()
	file, err := privateRoot.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure review output %s: %w", output, err)
	}
	cmd.Stdout, cmd.Stderr = file, file
	err = cmd.Run()
	if detailed {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 2 {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("terraform %s: %w", command, err)
	}
	return nil
}

func (a *App) tfxClean(args []string) error {
	base := a.getenv("TF_REPO")
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	all, deep, yes := false, false, false
	var roots []string
	for len(args) > 0 {
		switch args[0] {
		case "-r", "--all":
			all = true
			args = args[1:]
		case "--deep":
			deep = true
			args = args[1:]
		case "--yes":
			yes = true
			args = args[1:]
		case "--repo":
			if len(args) < 2 {
				return usage("tfx clean", "[--all|-r] [--deep] [--repo path] [root ...] [--yes]")
			}
			base, args = args[1], args[2:]
		default:
			if strings.HasPrefix(args[0], "-") {
				return usage("tfx clean", "[--all|-r] [--deep] [--repo path] [root ...] [--yes]")
			}
			roots = append(roots, args[0])
			args = args[1:]
		}
	}
	canonical, err := canonicalTFXBase(base)
	if err != nil {
		return err
	}
	scopes := []string{}
	if len(roots) > 0 {
		for _, root := range roots {
			p, err := tfxContainedPath(canonical, filepath.Join(canonical, root))
			if err != nil {
				return err
			}
			scopes = append(scopes, p)
		}
	} else {
		scopes = []string{canonical}
	}
	// Cleanup recognizes only bb's fixed legacy artifact names. Environment
	// variables are caller-controlled and therefore cannot authorize deletion
	// of an arbitrary source basename such as backend.tf.
	planNames := map[string]bool{"tfplan": true, "tfdestroyplan": true}
	targets := map[string]os.FileInfo{}
	for _, scope := range scopes {
		err := filepath.WalkDir(scope, func(path string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != scope && filepath.Dir(path) != scope && e.IsDir() && !all {
				return filepath.SkipDir
			}
			if e.Type()&os.ModeSymlink != 0 {
				if e.Name() == ".tf-review" || e.Name() == ".terraform" || planNames[e.Name()] {
					return invalid(fmt.Sprintf("refuse symlink cleanup target: %s", path))
				}
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if e.IsDir() && e.Name() == ".terraform" && !deep {
				return filepath.SkipDir
			}
			hit := e.Name() == ".tf-review" || (deep && e.Name() == ".terraform") || (!e.IsDir() && planNames[e.Name()])
			if hit {
				p, err := tfxContainedPath(canonical, path)
				if err != nil {
					return err
				}
				info, err := e.Info()
				if err != nil {
					return fmt.Errorf("inspect cleanup target %s: %w", path, err)
				}
				targets[p] = info
				if e.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("enumerate clean targets: %w", err)
		}
	}
	ordered := make([]string, 0, len(targets))
	for target := range targets {
		ordered = append(ordered, target)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		_, err := fmt.Fprintln(a.out, "No Terraform artifacts to clean.")
		return err
	}
	fmt.Fprintln(a.out, "Terraform cleanup targets:")
	for _, target := range ordered {
		fmt.Fprintf(a.out, "  %s\n", target)
	}
	if !yes {
		ok, err := a.confirmTFX("Remove these exact Terraform artifacts?")
		if err != nil {
			return err
		}
		if !ok {
			_, err = fmt.Fprintln(a.out, "Cancelled.")
			return err
		}
	}
	for _, target := range ordered {
		info, err := os.Lstat(target)
		if err != nil {
			return fmt.Errorf("re-observe cleanup target %s: %w", target, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return invalid(fmt.Sprintf("refuse symlink cleanup target: %s", target))
		}
		if !os.SameFile(targets[target], info) {
			return unavailable(fmt.Sprintf("cleanup target changed during confirmation: %s", target))
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove cleanup target %s: %w", target, err)
		}
	}
	_, err = fmt.Fprintf(a.out, "Removed %d Terraform artifact(s).\n", len(ordered))
	return err
}
