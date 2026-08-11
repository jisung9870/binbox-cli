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
)

type secretStore map[string]map[string]string

var regexpNonEnv = regexp.MustCompile(`[^A-Za-z0-9_]`)

const maxSecretValueBytes = 16 << 20

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
  bb sec                            Open the secret manager
  bb sec init
  bb sec list [service]
  bb sec set <service> <field> [--force]
  bb sec rename <service> <field> <new-field> [--yes]
  bb sec get <service> [field]
  bb sec copy [service] [field]
  bb sec env <service>
  bb sec exec <service> -- <command> [args...]
  bb sec rm <service> [field] [--yes]

Uses the existing age key/store format. Plaintext is never written to disk or journaled.
`)
		return e
	}
	if len(args) == 0 {
		return a.secManage()
	}
	switch args[0] {
	case "init":
		return a.secInit(args[1:])
	case "list", "ls":
		return a.secList(args[1:])
	case "set":
		return a.secSet(args[1:])
	case "rename":
		return a.secRenameField(args[1:])
	case "get":
		return a.secGet(args[1:])
	case "copy":
		return a.secCopy(args[1:])
	case "env":
		return a.secEnv(args[1:])
	case "exec":
		return a.secExec(args[1:])
	case "rm":
		return a.secRemove(args[1:])
	default:
		return invalid("unknown sec command")
	}
}
func validSecretName(s string) bool { return presetNameRE.MatchString(s) }
func (a *App) readSecretsSnapshot() (secretStore, []byte, error) {
	store, key := a.secPaths()
	storeInfo, e := os.Lstat(store)
	if e != nil {
		return nil, nil, fmt.Errorf("inspect encrypted secret store: %w", e)
	}
	if storeInfo.Mode()&os.ModeSymlink != 0 || !storeInfo.Mode().IsRegular() {
		return nil, nil, invalid("secret store must be a regular file, not a symlink")
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
	cipher, e := os.ReadFile(store)
	if e != nil {
		return nil, nil, fmt.Errorf("read encrypted secret store: %w", e)
	}
	cmd := a.command("age", "-d", "-i", key)
	cmd.Env = a.env
	cmd.Stdin = bytes.NewReader(cipher)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if e := cmd.Run(); e != nil {
		return nil, nil, fmt.Errorf("decrypt secret store: %s", strings.TrimSpace(stderr.String()))
	}
	var data secretStore
	if e := json.Unmarshal(out.Bytes(), &data); e != nil {
		return nil, nil, fmt.Errorf("parse decrypted secret store: %w", e)
	}
	if data == nil {
		data = secretStore{}
	}
	return data, cipher, nil
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
	store, key := a.secPaths()
	plain, e := json.Marshal(data)
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
	if e = os.MkdirAll(filepath.Dir(store), 0o700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(store), ".bb-secret-*.age")
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
		return fmt.Errorf("encrypt secret store: %w", e)
	}
	cipher, e := os.ReadFile(tmpPath)
	if e != nil {
		return e
	}
	return withFileLock(store, func() error {
		old, readErr := os.ReadFile(store)
		if os.IsNotExist(readErr) {
			old = nil
		} else if readErr != nil {
			return readErr
		}
		if !bytes.Equal(old, expected) {
			return unavailable("encrypted secret store changed during update; retry")
		}
		if e = a.backupCiphertext(old); e != nil {
			return e
		}
		return writeBytesAtomic(store, cipher)
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
	var values []string
	if len(args) == 0 {
		for s := range d {
			values = append(values, s)
		}
	} else {
		fields, ok := d[args[0]]
		if !ok {
			return invalid("secret service not found: " + args[0])
		}
		for f := range fields {
			values = append(values, f)
		}
	}
	sort.Strings(values)
	for _, v := range values {
		fmt.Fprintln(a.out, v)
	}
	return nil
}
func (a *App) secGet(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usage("sec get", "<service> [field]")
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
	_, e = fmt.Fprintln(a.out, d[args[0]][field])
	return e
}
func (a *App) secSet(args []string) error {
	args, force := takeFlag(args, "--force")
	if len(args) != 2 || !validSecretName(args[0]) || !validSecretName(args[1]) {
		return usage("sec set", "<service> <field> [--force]")
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
	value, e := a.readSecretValue()
	if e != nil {
		return e
	}
	if len(value) == 0 {
		return invalid("secret value must not be empty")
	}
	if d[args[0]] == nil {
		d[args[0]] = map[string]string{}
	}
	d[args[0]][args[1]] = string(value)
	return a.writeSecrets(d, old)
}

func (a *App) secRenameField(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 3 || !validSecretName(args[0]) || !validSecretName(args[1]) || !validSecretName(args[2]) {
		return usage("sec rename", "<service> <field> <new-field> [--yes]")
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
	return a.writeSecrets(data, old)
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
	if len(args) < 1 || len(args) > 2 {
		return usage("sec rm", "<service> [field] [--yes]")
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
	if !yes {
		ok, e := a.confirmAction("Remove encrypted secret entry?")
		if e != nil {
			return e
		}
		if !ok {
			return invalid("secret removal cancelled")
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
	return a.writeSecrets(d, old)
}
func (a *App) secEnv(args []string) error {
	if len(args) != 1 {
		return usage("sec env", "<service>")
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

func secretServiceChoices(data secretStore) []selectChoice {
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
		choices = append(choices, selectChoice{
			Value:       service,
			Label:       service,
			Description: fmt.Sprintf("%d %s", len(fields), fieldWord),
			SearchText:  strings.Join(fieldNames, " "),
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Label < choices[j].Label
	})
	return choices
}

func secretFieldChoices(service string, fields map[string]string) []selectChoice {
	choices := make([]selectChoice, 0, len(fields))
	for field := range fields {
		choices = append(choices, selectChoice{
			Value:      field,
			Label:      field,
			SearchText: service + "/" + field,
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
	services := secretServiceChoices(data)
	if len(services) == 0 {
		return unavailable("secret store is empty; add one with 'bb sec set <service> <field>'")
	}
	for {
		serviceResult, selectErr := a.selectOneOutcome("Secret service", services)
		if selectErr != nil {
			return selectErr
		}
		if serviceResult.Interrupted || serviceResult.Value == "" {
			return nil
		}
		service := serviceResult.Value
		fields := data[service]
		for {
			fieldResult, fieldErr := a.selectOneOutcome("Field in "+service, secretFieldChoices(service, fields))
			if fieldErr != nil {
				return fieldErr
			}
			if fieldResult.Interrupted {
				return nil
			}
			if fieldResult.Value == "" {
				break
			}
			field := fieldResult.Value
			for {
				actionResult, actionErr := a.selectOneOutcome("Secret action", secretActionChoices(service, field, len(fields)))
				if actionErr != nil {
					return actionErr
				}
				if actionResult.Interrupted {
					return nil
				}
				if actionResult.Value == "" {
					break
				}
				switch actionResult.Value {
				case "copy":
					return a.secCopy([]string{service, field})
				case "replace":
					return a.secSet([]string{service, field})
				case "rename-field":
					newField, promptErr := a.promptSecretFieldName()
					if promptErr != nil {
						return promptErr
					}
					if newField == "" {
						continue
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
		}
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
		{Value: "remove-field", Label: "Remove field", Description: service + "/" + field},
		{Value: "remove-service", Label: "Remove service", Description: fmt.Sprintf("%s · %d %s", service, fieldCount, fieldWord)},
	}
}

func (a *App) promptSecretFieldName() (string, error) {
	if _, e := fmt.Fprint(a.err, "New field name [Enter=cancel]: "); e != nil {
		return "", e
	}
	value, e := readLine(a.in)
	value = strings.TrimSpace(value)
	if e != nil && value == "" {
		return "", fmt.Errorf("read new secret field name: %w", e)
	}
	if value == "" {
		return "", nil
	}
	if !validSecretName(value) {
		return "", invalid("secret field names may contain only letters, digits, dot, underscore, and hyphen")
	}
	return value, nil
}

func (a *App) secCopy(args []string) error {
	if len(args) > 2 {
		return usage("sec copy", "[service] [field]")
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
		picked, e := a.selectOne("Secret service", secretServiceChoices(d))
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
		field, e = a.selectOne("Field in "+svc, secretFieldChoices(svc, fields))
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
	for _, name := range []string{"pbcopy", "clip.exe", "wl-copy", "xclip"} {
		if _, e := a.lookPath(name); e == nil {
			argv := []string{}
			if name == "xclip" {
				argv = []string{"-selection", "clipboard"}
			}
			cmd := a.command(name, argv...)
			cmd.Env = a.env
			cmd.Stdin = strings.NewReader(d[svc][field])
			cmd.Stderr = a.err
			return cmd.Run()
		}
	}
	return unavailable("no clipboard command found")
}
