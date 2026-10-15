package bb

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type secretStore map[string]map[string]string

var regexpNonEnv = regexp.MustCompile(`[^A-Za-z0-9_]`)

const maxSecretValueBytes = 16 << 20

const (
	secAddSecretAction = "::add-secret::"
	secAddFieldAction  = "::add-field::"
)

func (a *App) secPaths() (string, string) {
	store := a.getenv("BINBOX_SECRETS_FILE")
	key := a.getenv("BINBOX_AGE_KEY")
	base := filepath.Join(a.getenv("HOME"), ".config", "binbox")
	if store == "" {
		store = filepath.Join(base, "secrets.json.age")
	}
	if key == "" {
		key = filepath.Join(base, "age.key")
	}
	return store, key
}
func (a *App) sec(args []string) error {
	if helpRequested(args) {
		_, e := fmt.Fprint(a.out, `Usage:
  bb sec                            Open the add/manage secret manager
  bb sec init
  bb sec list [service]             Names with expiry badges (D-12, expired)
  bb sec add <service/field> [--paste] [--expires DATE|90d] [--url URL] [--note TEXT] [--force]
  bb sec set <service/field> ...    Same as add
  bb sec meta <service/field> [--expires DATE|90d|none] [--url URL|none] [--note TEXT|none] [--json]
  bb sec check [--within 14d] [--all] [--json|--quiet]
  bb sec check --unused 90d [--json]   Fields bb has not handed out for 90 days
  bb sec rotate <service/field> [--paste] [--expires DATE|90d]   Previous value kept 7 days
  bb sec rotate <service/field> --undo
  bb sec where <service[/field]> [--json]   wenv presets, MCP servers and exec rules using it
  bb sec import <file.env|-> --service <service> [--only KEY,KEY] [--force]
  bb sec meta <service/field> --verify gitlab=https://host|github|jenkins=URL|awx=URL [--verify-user FIELD]
  bb sec verify [service[/field]] [--json]   Ask the issuer who the token is; records reported expiry
  bb sec rename <service/field> <new-field> [--yes]
  bb sec get <service[/field]>
  bb sec copy [service[/field]] [--keep]
  bb sec env <service>
  bb sec exec <service> -- <command> [args...]
  bb sec rm <service[/field]> [--purge] [--yes]
  bb sec trash [--empty [--yes]]
  bb sec restore [service[/field]]
  bb sec harden claude|codex [--print] [--yes]   Deny agent shells the value-printing commands

"service field" still works wherever "service/field" is shown.
--paste reads the value from the clipboard and clears it after storing.
copy clears the clipboard after 30s (BB_SEC_CLIPBOARD_CLEAR=<seconds>, 0 disables).
rm moves fields to an encrypted trash kept for 7 days; --purge deletes at once.
Metadata (expiry, url, note) is stored without values next to the store, and
JWT values get their expiry from the exp claim. check reads metadata only and
exits 1 when anything is expired or expiring.

Uses the existing age key/store format. Plaintext is never written to disk or journaled.
get/env/copy emit secret material and run only on a terminal. Automation resolves
secrets through "bb sec exec", "bb mcp serve", or an explicit BB_ALLOW_SECRET_OUTPUT=1.
`)
		return e
	}
	if len(args) == 0 {
		return a.secManage()
	}
	rest := args[1:]
	switch args[0] {
	case "set", "add", "meta", "rename", "get", "copy", "rm", "restore", "rotate", "where", "verify":
		rest = expandSecretPath(rest)
	}
	switch args[0] {
	case "init":
		return a.secInit(rest)
	case "list", "ls":
		return a.secList(rest)
	case "set", "add":
		return a.secSet(rest)
	case "meta":
		return a.secMeta(rest)
	case "check":
		return a.secCheck(rest)
	case "rename":
		return a.secRenameField(rest)
	case "get":
		return a.secGet(rest)
	case "copy":
		return a.secCopy(rest)
	case "env":
		return a.secEnv(rest)
	case "exec":
		return a.secExec(rest)
	case "rm":
		return a.secRemove(rest)
	case "trash":
		return a.secTrash(rest)
	case "restore":
		return a.secRestore(rest)
	case "harden":
		return a.secHarden(rest)
	case "where":
		return a.secWhere(rest)
	case "import":
		return a.secImport(rest)
	case "rotate":
		return a.secRotate(rest)
	case "verify":
		return a.secVerify(rest)
	case clipboardClearCommand:
		return a.secClipboardClear(rest)
	default:
		return invalid("unknown sec command")
	}
}
func validSecretName(s string) bool { return presetNameRE.MatchString(s) }

// secretValueOptions take the next argument as their value, so a value such as
// a note containing "/" is never mistaken for a service/field path.
var secretValueOptions = map[string]bool{"--expires": true, "--url": true, "--note": true}

// expandSecretPath splits the first "service/field" positional argument into
// the two arguments the subcommands already accept.
func expandSecretPath(args []string) []string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if secretValueOptions[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		service, field, ok := strings.Cut(arg, "/")
		if !ok || service == "" || field == "" || strings.Contains(field, "/") {
			return args
		}
		out := append([]string{}, args[:i]...)
		out = append(out, service, field)
		return append(out, args[i+1:]...)
	}
	return args
}
func (a *App) readSecretsSnapshot() (secretStore, []byte, error) {
	store, _ := a.secPaths()
	plain, cipher, e := a.decryptSecretFile(store, "secret store")
	if e != nil {
		return nil, nil, e
	}
	var data secretStore
	if e := json.Unmarshal(plain, &data); e != nil {
		return nil, nil, fmt.Errorf("parse decrypted secret store: %w", e)
	}
	if data == nil {
		data = secretStore{}
	}
	return data, cipher, nil
}

// decryptSecretFile returns the plaintext and the exact ciphertext of one age
// file encrypted to the bb key. The ciphertext is the optimistic-concurrency
// token passed back to writeEncryptedJSON.
func (a *App) decryptSecretFile(path, label string) ([]byte, []byte, error) {
	_, key := a.secPaths()
	info, e := os.Lstat(path)
	if e != nil {
		return nil, nil, fmt.Errorf("inspect encrypted %s: %w", label, e)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, invalid(label + " must be a regular file, not a symlink")
	}
	keyInfo, e := os.Lstat(key)
	if e != nil {
		return nil, nil, fmt.Errorf("inspect age key: %w", e)
	}
	if keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() {
		return nil, nil, invalid("age key must be a regular file, not a symlink")
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return nil, nil, invalid("age key permissions must be owner-only (0600)")
	}
	if _, e := a.lookPath("age"); e != nil {
		return nil, nil, unavailable("age is required for bb sec")
	}
	cipher, e := os.ReadFile(path)
	if e != nil {
		return nil, nil, fmt.Errorf("read encrypted %s: %w", label, e)
	}
	cmd := a.command("age", "-d", "-i", key)
	cmd.Env = a.env
	cmd.Stdin = bytes.NewReader(cipher)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if e := cmd.Run(); e != nil {
		return nil, nil, fmt.Errorf("decrypt %s: %s", label, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), cipher, nil
}
func (a *App) readSecrets() (secretStore, error) { d, _, e := a.readSecretsSnapshot(); return d, e }
func (a *App) backupCiphertext(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	_, state, e := a.paths()
	if e != nil {
		return e
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))
	return writeBytesAtomic(filepath.Join(state, "secret-store-backups", sum+".age"), b)
}
func (a *App) writeSecrets(data secretStore, expected []byte) error {
	store, _ := a.secPaths()
	return a.writeEncryptedJSON(store, data, expected, true)
}

// writeEncryptedJSON encrypts v to the bb key and replaces path only when its
// current ciphertext still equals expected, so concurrent edits fail instead of
// silently overwriting each other.
func (a *App) writeEncryptedJSON(path string, v any, expected []byte, backup bool) error {
	_, key := a.secPaths()
	plain, e := json.Marshal(v)
	if e != nil {
		return e
	}
	recipient, e := a.readCommand("age-keygen", "-y", key)
	if e != nil {
		return e
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return invalid("age key did not produce a recipient")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".bb-secret-*.age")
	if e != nil {
		return e
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	cmd := a.command("age", "-r", recipient, "-o", tmpPath)
	cmd.Env = a.env
	cmd.Stdin = bytes.NewReader(append(plain, '\n'))
	cmd.Stderr = a.err
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("encrypt %s: %w", filepath.Base(path), e)
	}
	cipher, e := os.ReadFile(tmpPath)
	if e != nil {
		return e
	}
	return withFileLock(path, func() error {
		old, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			old = nil
		} else if readErr != nil {
			return readErr
		}
		if !bytes.Equal(old, expected) {
			return unavailable("encrypted " + filepath.Base(path) + " changed during update; retry")
		}
		if backup {
			if e = a.backupCiphertext(old); e != nil {
				return e
			}
		}
		return writeBytesAtomic(path, cipher)
	})
}
func (a *App) secInit(args []string) error {
	if len(args) != 0 {
		return usage("sec init", "")
	}
	store, key := a.secPaths()
	if _, e := os.Lstat(store); e == nil {
		return invalid("secret store already exists")
	} else if !os.IsNotExist(e) {
		return e
	}
	if _, e := a.lookPath("age"); e != nil {
		return unavailable("age is required")
	}
	if _, e := a.lookPath("age-keygen"); e != nil {
		return unavailable("age-keygen is required")
	}
	if e := os.MkdirAll(filepath.Dir(key), 0o700); e != nil {
		return e
	}
	info, e := os.Lstat(key)
	switch {
	case os.IsNotExist(e):
		cmd := a.command("age-keygen", "-o", key)
		cmd.Env, cmd.Stdout, cmd.Stderr = a.env, a.err, a.err
		if e := cmd.Run(); e != nil {
			return e
		}
	case e != nil:
		return e
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return invalid("age key must be a regular file")
	}
	if e := os.Chmod(key, 0o600); e != nil {
		return e
	}
	return a.writeSecrets(secretStore{}, nil)
}
func resolveSecretField(data secretStore, svc, field string) (string, error) {
	fields, ok := data[svc]
	if !ok {
		return "", invalid("secret service not found: " + svc)
	}
	if field != "" {
		if _, ok := fields[field]; !ok {
			return "", invalid("secret field not found")
		}
		return field, nil
	}
	if len(fields) != 1 {
		return "", invalid("field is required when service has zero or multiple fields")
	}
	for f := range fields {
		return f, nil
	}
	return "", invalid("secret field not found")
}
func (a *App) secList(args []string) error {
	if len(args) > 1 {
		return usage("sec list", "[service]")
	}
	d, e := a.readSecrets()
	if e != nil {
		return e
	}
	meta, e := a.readSecretMeta()
	if e != nil {
		return e
	}
	today := a.secretToday()
	var values []string
	badges := map[string]string{}
	if len(args) == 0 {
		for s, fields := range d {
			values = append(values, s)
			badges[s] = worstServiceStatus(s, fields, meta, today).badge()
		}
	} else {
		fields, ok := d[args[0]]
		if !ok {
			return invalid("secret service not found: " + args[0])
		}
		for f := range fields {
			values = append(values, f)
			key := secretKey(args[0], f)
			badges[f] = expiryStatus(key, meta[key], today, defaultExpiryWindowDays).badge()
		}
	}
	sort.Strings(values)
	return a.printSecretNames(values, badges)
}
func (a *App) secGet(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usage("sec get", "<service> [field]")
	}
	if e := a.requireSecretOutputConsent("sec get"); e != nil {
		return e
	}
	d, e := a.readSecrets()
	if e != nil {
		return e
	}
	field := ""
	if len(args) == 2 {
		field = args[1]
	}
	field, e = resolveSecretField(d, args[0], field)
	if e != nil {
		return e
	}
	a.markSecretsUsed([]string{secretKey(args[0], field)})
	_, e = fmt.Fprintln(a.out, d[args[0]][field])
	return e
}
func (a *App) secSet(args []string) error {
	args, force := takeFlag(args, "--force")
	args, paste := takeFlag(args, "--paste")
	args, opts, e := takeSecretMetaOptions(args)
	if e != nil {
		return e
	}
	if len(args) != 2 || !validSecretName(args[0]) || !validSecretName(args[1]) {
		return usage("sec set", "<service/field> [--paste] [--expires DATE|90d] [--url URL] [--note TEXT] [--force]")
	}
	// Validate metadata before asking for a value so a typo costs nothing.
	if _, e := opts.apply(secretMeta{}, a.secretToday(), ""); e != nil {
		return e
	}
	d, old, e := a.readSecretsSnapshot()
	if e != nil {
		return e
	}
	if _, exists := d[args[0]][args[1]]; exists && !force {
		confirmed, confirmErr := a.confirmAction("Replace encrypted secret field " + args[0] + "/" + args[1] + "?")
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return invalid("secret replacement cancelled")
		}
	}
	var value []byte
	pastedDigest := ""
	if paste {
		value, e = a.readClipboard()
		// Compare against the clipboard as read, not the trimmed value.
		value = bytes.TrimRight(value, "\r\n")
		pastedDigest = clipboardDigest(value)
	} else {
		value, e = a.readSecretValue()
	}
	if e != nil {
		return e
	}
	if len(value) == 0 {
		if paste {
			return invalid("clipboard is empty")
		}
		return invalid("secret value must not be empty")
	}
	if d[args[0]] == nil {
		d[args[0]] = map[string]string{}
	}
	d[args[0]][args[1]] = string(value)
	if e := a.writeSecrets(d, old); e != nil {
		return e
	}
	key := secretKey(args[0], args[1])
	if paste {
		if e := a.clearClipboardIfUnchanged(pastedDigest); e != nil {
			fmt.Fprintln(a.err, "Stored "+key+", but could not clear the clipboard: "+e.Error())
		} else {
			fmt.Fprintln(a.err, "Stored "+key+" from the clipboard; clipboard cleared.")
		}
	}
	jwtDate, _ := jwtExpiry(string(value))
	if !opts.any() && jwtDate == "" {
		if meta, metaErr := a.readSecretMeta(); metaErr == nil && meta[key].ExpiresAt != "" {
			fmt.Fprintf(a.err, "Expiry is still %s; update it with: bb sec meta %s --expires <date|90d|none>\n", meta[key].ExpiresAt, key)
		}
		return nil
	}
	e = a.updateSecretMeta(func(entries map[string]secretMeta) error {
		meta, applyErr := opts.apply(entries[key], a.secretToday(), jwtDate)
		if applyErr != nil {
			return applyErr
		}
		meta.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		entries[key] = meta
		return nil
	})
	if e != nil {
		return fmt.Errorf("stored %s, but could not update its metadata: %w", key, e)
	}
	if jwtDate != "" && !opts.hasExpires {
		fmt.Fprintln(a.err, "Expiry "+jwtDate+" read from the token's exp claim.")
	}
	return nil
}

func (a *App) secRenameField(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 3 || !validSecretName(args[0]) || !validSecretName(args[1]) || !validSecretName(args[2]) {
		return usage("sec rename", "<service/field> <new-field> [--yes]")
	}
	service, field, newField := args[0], args[1], args[2]
	if field == newField {
		return invalid("new secret field name must differ from the current name")
	}
	data, old, e := a.readSecretsSnapshot()
	if e != nil {
		return e
	}
	fields, ok := data[service]
	if !ok {
		return invalid("secret service not found: " + service)
	}
	value, ok := fields[field]
	if !ok {
		return invalid("secret field not found")
	}
	if _, exists := fields[newField]; exists {
		return invalid("target secret field already exists")
	}
	if !yes {
		confirmed, confirmErr := a.confirmAction("Rename encrypted secret field " + service + "/" + field + " to " + newField + "?")
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return invalid("secret field rename cancelled")
		}
	}
	fields[newField] = value
	delete(fields, field)
	if e := a.writeSecrets(data, old); e != nil {
		return e
	}
	a.warnSecretReferences(service, field, "renamed to "+newField)
	return a.renameSecretMeta(service, field, newField)
}

// secretOutputOverrideEnv re-enables plaintext secret output for automation that
// has no terminal and accepts the exposure. It is deliberately explicit so a
// permission rule or an audit can look for it.
const secretOutputOverrideEnv = "BB_ALLOW_SECRET_OUTPUT"

// terminalAttached reports whether bb is talking to a human terminal. stdout is
// deliberately excluded: `eval "$(bb wenv dev)"` pipes stdout while stdin and
// stderr stay attached to the operator's terminal.
func (a *App) terminalAttached() bool {
	input, ok := a.in.(*os.File)
	if !ok || !a.isTerminal(input.Fd()) {
		return false
	}
	errOut, ok := a.err.(*os.File)
	return ok && a.isTerminal(errOut.Fd())
}

// requireSecretOutputConsent refuses to emit resolved secret material into a
// stream no human is watching. Agent tool calls and captured pipelines land
// there, and a value printed once stays in their transcript forever.
func (a *App) requireSecretOutputConsent(command string) error {
	if a.terminalAttached() || a.getenv(secretOutputOverrideEnv) == "1" {
		return nil
	}
	return invalid(fmt.Sprintf(
		"bb %s emits secret material and requires a terminal; run 'bb sec exec' or 'bb mcp serve' from automation, or set %s=1 to accept the exposure",
		command, secretOutputOverrideEnv))
}

func (a *App) readSecretValue() ([]byte, error) {
	if input, ok := a.in.(*os.File); ok && a.isTerminal(input.Fd()) {
		if _, e := fmt.Fprint(a.err, "Secret value: "); e != nil {
			return nil, e
		}
		value, e := a.readPassword(input.Fd())
		if _, newlineErr := fmt.Fprintln(a.err); e == nil && newlineErr != nil {
			e = newlineErr
		}
		if e != nil {
			return nil, fmt.Errorf("read secret value: %w", e)
		}
		if len(value) > maxSecretValueBytes {
			return nil, invalid("secret value exceeds 16 MiB")
		}
		return value, nil
	}
	value, e := io.ReadAll(io.LimitReader(a.in, maxSecretValueBytes+1))
	if e != nil {
		return nil, e
	}
	value = bytes.TrimSuffix(value, []byte("\n"))
	if len(value) > maxSecretValueBytes {
		return nil, invalid("secret value exceeds 16 MiB")
	}
	return value, nil
}
func (a *App) secRemove(args []string) error {
	args, yes := takeFlag(args, "--yes")
	args, purge := takeFlag(args, "--purge")
	if len(args) < 1 || len(args) > 2 {
		return usage("sec rm", "<service[/field]> [--purge] [--yes]")
	}
	d, old, e := a.readSecretsSnapshot()
	if e != nil {
		return e
	}
	if _, ok := d[args[0]]; !ok {
		return invalid("secret service not found")
	}
	if len(args) == 2 {
		if _, ok := d[args[0]][args[1]]; !ok {
			return invalid("secret field not found")
		}
	}
	fields := []string{}
	if len(args) == 2 {
		fields = append(fields, args[1])
	} else {
		for field := range d[args[0]] {
			fields = append(fields, field)
		}
		sort.Strings(fields)
	}
	keys := make([]string, len(fields))
	for i, field := range fields {
		keys[i] = secretKey(args[0], field)
	}
	// Moving one field to the restorable trash needs no confirmation; a whole
	// service or a permanent purge still defaults to cancel.
	if !yes && (purge || len(args) == 1) {
		question := fmt.Sprintf("Move %s (%d field(s)) to the trash?", args[0], len(fields))
		if purge {
			question = "Permanently remove " + strings.Join(keys, ", ") + "?"
		}
		ok, e := a.confirmAction(question)
		if e != nil {
			return e
		}
		if !ok {
			return invalid("secret removal cancelled")
		}
	}
	meta, e := a.readSecretMeta()
	if e != nil {
		return e
	}
	if purge {
		if e := a.purgeFromTrash(keys); e != nil {
			return fmt.Errorf("purge trashed copies (nothing removed): %w", e)
		}
	} else {
		now := a.now().UTC()
		entries := make([]secretTrashEntry, len(fields))
		for i, field := range fields {
			entries[i] = secretTrashEntry{Service: args[0], Field: field, Value: d[args[0]][field], DeletedAt: now}
			if m, ok := meta[keys[i]]; ok {
				entries[i].Meta = &m
			}
		}
		if e := a.moveSecretsToTrash(entries); e != nil {
			return fmt.Errorf("move to trash (nothing removed; use --purge to skip the trash): %w", e)
		}
	}
	if len(args) == 1 {
		delete(d, args[0])
	} else {
		delete(d[args[0]], args[1])
		if len(d[args[0]]) == 0 {
			delete(d, args[0])
		}
	}
	if e := a.writeSecrets(d, old); e != nil {
		return e
	}
	if e := a.dropSecretMeta(keys); e != nil {
		return e
	}
	field := ""
	if len(args) == 2 {
		field = args[1]
	}
	a.warnSecretReferences(args[0], field, "removed")
	if !purge {
		fmt.Fprintf(a.err, "Moved %s to the trash; restore within 7 days with: bb sec restore %s\n",
			strings.Join(keys, ", "), strings.Join(args, "/"))
	}
	return nil
}
func (a *App) secEnv(args []string) error {
	if len(args) != 1 {
		return usage("sec env", "<service>")
	}
	if e := a.requireSecretOutputConsent("sec env"); e != nil {
		return e
	}
	d, e := a.readSecrets()
	if e != nil {
		return e
	}
	fields, ok := d[args[0]]
	if !ok {
		return invalid("secret service not found")
	}
	variables, e := secretEnvironmentVariables(args[0], fields)
	if e != nil {
		return e
	}
	a.markSecretsUsed(serviceKeys(args[0], fields))
	for _, variable := range variables {
		if _, e := fmt.Fprintf(a.out, "export %s=%s\n", variable.Name, shellQuote(variable.Value)); e != nil {
			return e
		}
	}
	return nil
}

type secretEnvironmentVariable struct {
	Name  string
	Value string
}

func secretEnvironmentVariables(service string, fields map[string]string) ([]secretEnvironmentVariable, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := make(map[string]string, len(keys))
	variables := make([]secretEnvironmentVariable, 0, len(keys))
	for _, key := range keys {
		name := secretEnvName(service, key)
		if previous, exists := seen[name]; exists {
			return nil, invalid(fmt.Sprintf("secret fields %q and %q produce the same environment name %s", previous, key, name))
		}
		seen[name] = key
		variables = append(variables, secretEnvironmentVariable{Name: name, Value: fields[key]})
	}
	return variables, nil
}

func secretEnvName(service, field string) string {
	name := strings.ToUpper(regexpNonEnv.ReplaceAllString(service+"_"+field, "_"))
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

func (a *App) secExec(args []string) error {
	if len(args) < 3 || args[1] != "--" || args[0] == "" {
		return usage("sec exec", "<service> -- <command> [args...]")
	}
	data, e := a.readSecrets()
	if e != nil {
		return e
	}
	fields, ok := data[args[0]]
	if !ok {
		return invalid("secret service not found: " + safeTerminalText(args[0]))
	}
	if len(fields) == 0 {
		return invalid("secret service has no fields: " + safeTerminalText(args[0]))
	}
	variables, e := secretEnvironmentVariables(args[0], fields)
	if e != nil {
		return e
	}
	a.markSecretsUsed(serviceKeys(args[0], fields))
	cmd := a.command(args[2], args[3:]...)
	cmd.Env = overlaySecretEnvironment(a.env, variables)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.err
	if e := cmd.Run(); e != nil {
		return fmt.Errorf("run with secret service %s: %w", safeTerminalText(args[0]), e)
	}
	return nil
}

func overlaySecretEnvironment(base []string, variables []secretEnvironmentVariable) []string {
	replaced := make(map[string]bool, len(variables))
	for _, variable := range variables {
		replaced[variable.Name] = true
	}
	env := make([]string, 0, len(base)+len(variables))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !replaced[key] {
			env = append(env, entry)
		}
	}
	for _, variable := range variables {
		env = append(env, variable.Name+"="+variable.Value)
	}
	return env
}

func secretServiceChoices(data secretStore, meta map[string]secretMeta, today time.Time) []selectChoice {
	choices := make([]selectChoice, 0, len(data))
	for service, fields := range data {
		if len(fields) == 0 {
			continue
		}
		fieldNames := make([]string, 0, len(fields))
		for field := range fields {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)
		fieldWord := "fields"
		if len(fields) == 1 {
			fieldWord = "field"
		}
		description := fmt.Sprintf("%d %s", len(fields), fieldWord)
		if badge := worstServiceStatus(service, fields, meta, today).badge(); badge != "" {
			description += " · " + badge
		}
		choices = append(choices, selectChoice{
			Value:       service,
			Label:       service,
			Description: description,
			SearchText:  strings.Join(fieldNames, " "),
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Label < choices[j].Label
	})
	return choices
}

func secretFieldChoices(service string, fields map[string]string, meta map[string]secretMeta, today time.Time) []selectChoice {
	choices := make([]selectChoice, 0, len(fields))
	for field := range fields {
		key := secretKey(service, field)
		choices = append(choices, selectChoice{
			Value:       field,
			Label:       field,
			Description: expiryStatus(key, meta[key], today, defaultExpiryWindowDays).badge(),
			SearchText:  key,
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Label < choices[j].Label
	})
	return choices
}

func (a *App) secManage() error {
	data, e := a.readSecrets()
	if e != nil {
		return e
	}
	meta, e := a.readSecretMeta()
	if e != nil {
		return e
	}
	today := a.secretToday()
	services := secretServiceChoices(data, meta, today)
	services = append(services, selectChoice{
		Value:       secAddSecretAction,
		Label:       "Add secret",
		Description: "Create a service and field",
	})

	// Service -> Field -> Action. Each level is derived from the values already
	// chosen, so a field list can only ever belong to the selected service.
	// Selecting never mutates: the walk returns a path and the action runs after
	// the selector has closed.
	root := selectStage{Prompt: "Secret service", Choices: services}
	next := func(path []string) *selectStage {
		switch len(path) {
		case 1:
			service := path[0]
			if service == secAddSecretAction {
				return nil
			}
			fields := secretFieldChoices(service, data[service], meta, today)
			fields = append(fields, selectChoice{
				Value:       secAddFieldAction,
				Label:       "Add field",
				Description: service,
			})
			return &selectStage{
				Prompt:  "Field in " + service,
				Choices: fields,
			}
		case 2:
			service, field := path[0], path[1]
			if field == secAddFieldAction {
				return nil
			}
			return &selectStage{
				Prompt:  "Action for " + service + "/" + field,
				Choices: secretActionChoices(service, field, len(data[service])),
			}
		default:
			return nil
		}
	}

	outcome, e := a.selectStages(root, next)
	if e != nil {
		return e
	}
	if outcome.Cancelled || len(outcome.Path) == 0 {
		return nil
	}
	if outcome.Path[0] == secAddSecretAction {
		service, promptErr := a.promptSecretName("Service name")
		if promptErr != nil || service == "" {
			return promptErr
		}
		if _, exists := data[service]; exists {
			return invalid("secret service already exists: " + service)
		}
		field, promptErr := a.promptSecretName("Field name")
		if promptErr != nil || field == "" {
			return promptErr
		}
		return a.secSet([]string{service, field})
	}
	if len(outcome.Path) == 2 && outcome.Path[1] == secAddFieldAction {
		field, promptErr := a.promptSecretName("Field name")
		if promptErr != nil || field == "" {
			return promptErr
		}
		if _, exists := data[outcome.Path[0]][field]; exists {
			return invalid("secret field already exists: " + outcome.Path[0] + "/" + field)
		}
		return a.secSet([]string{outcome.Path[0], field})
	}
	if len(outcome.Path) < 3 {
		return nil
	}

	service, field, action := outcome.Path[0], outcome.Path[1], outcome.Path[2]
	switch action {
	case "copy":
		return a.secCopy([]string{service, field})
	case "replace":
		return a.secSet([]string{service, field})
	case "set-expiry":
		value, promptErr := a.promptSecretText("Expiry (YYYY-MM-DD, 90d, none)")
		if promptErr != nil || value == "" {
			return promptErr
		}
		return a.secMeta([]string{service, field, "--expires", value})
	case "rename-field":
		newField, promptErr := a.promptSecretFieldName()
		if promptErr != nil {
			return promptErr
		}
		if newField == "" {
			return nil
		}
		return a.secRenameField([]string{service, field, newField})
	case "remove-field":
		return a.secRemove([]string{service, field})
	case "remove-service":
		return a.secRemove([]string{service})
	default:
		return invalid("unknown secret action")
	}
}

func secretActionChoices(service, field string, fieldCount int) []selectChoice {
	fieldWord := "fields"
	if fieldCount == 1 {
		fieldWord = "field"
	}
	return []selectChoice{
		{Value: "copy", Label: "Copy to clipboard", Description: service + "/" + field},
		{Value: "replace", Label: "Replace value", Description: "Confirm, then enter without echo"},
		{Value: "rename-field", Label: "Rename field", Description: service + "/" + field},
		{Value: "remove-field", Label: "Remove field", Description: "Move to trash · restorable 7 days"},
		{Value: "remove-service", Label: "Remove service", Description: fmt.Sprintf("%s · %d %s", service, fieldCount, fieldWord)},
		{Value: "set-expiry", Label: "Set expiry", Description: "Date or duration, stored without the value"},
	}
}

func (a *App) promptSecretText(label string) (string, error) {
	if _, e := fmt.Fprintf(a.err, "%s [Enter=cancel]: ", label); e != nil {
		return "", e
	}
	value, e := readLine(a.in)
	value = strings.TrimSpace(value)
	if e != nil && value == "" {
		return "", fmt.Errorf("read %s: %w", strings.ToLower(label), e)
	}
	return value, nil
}

func (a *App) promptSecretFieldName() (string, error) {
	return a.promptSecretName("New field name")
}

func (a *App) promptSecretName(label string) (string, error) {
	if _, e := fmt.Fprintf(a.err, "%s [Enter=cancel]: ", label); e != nil {
		return "", e
	}
	value, e := readLine(a.in)
	value = strings.TrimSpace(value)
	if e != nil && value == "" {
		return "", fmt.Errorf("read secret name: %w", e)
	}
	if value == "" {
		return "", nil
	}
	if !validSecretName(value) {
		return "", invalid("secret names may contain only letters, digits, dot, underscore, and hyphen")
	}
	return value, nil
}

func (a *App) secCopy(args []string) error {
	args, keep := takeFlag(args, "--keep")
	if len(args) > 2 {
		return usage("sec copy", "[service[/field]] [--keep]")
	}
	if e := a.requireSecretOutputConsent("sec copy"); e != nil {
		return e
	}
	d, e := a.readSecrets()
	if e != nil {
		return e
	}
	svc, field := "", ""
	if len(args) > 0 {
		svc = args[0]
	}
	if len(args) > 1 {
		field = args[1]
	}
	if svc == "" {
		meta, metaErr := a.readSecretMeta()
		if metaErr != nil {
			return metaErr
		}
		picked, e := a.selectOne("Secret service", secretServiceChoices(d, meta, a.secretToday()))
		if e != nil {
			return e
		}
		if picked == "" {
			return nil
		}
		svc = picked
	}
	fields, ok := d[svc]
	if !ok {
		return invalid("secret service not found: " + svc)
	}
	if field == "" && len(fields) > 1 {
		meta, metaErr := a.readSecretMeta()
		if metaErr != nil {
			return metaErr
		}
		field, e = a.selectOne("Field in "+svc, secretFieldChoices(svc, fields, meta, a.secretToday()))
		if e != nil {
			return e
		}
		if field == "" {
			return nil
		}
	}
	field, e = resolveSecretField(d, svc, field)
	if e != nil {
		return e
	}
	value := d[svc][field]
	if e := a.writeClipboard(value); e != nil {
		return e
	}
	a.markSecretsUsed([]string{secretKey(svc, field)})
	seconds := a.clipboardClearSeconds()
	if keep || seconds == 0 {
		return nil
	}
	if e := a.scheduleClipboardClear(value, seconds); e != nil {
		fmt.Fprintln(a.err, "Copied "+secretKey(svc, field)+"; automatic clipboard clear failed: "+e.Error())
		return nil
	}
	fmt.Fprintf(a.err, "Copied %s; clipboard clears in %ds.\n", secretKey(svc, field), seconds)
	return nil
}
