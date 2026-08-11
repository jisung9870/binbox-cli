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
	if helpRequested(args) || len(args) == 0 {
		_, e := fmt.Fprint(a.out, `Usage:
  bb sec init
  bb sec list [service]
  bb sec set <service> <field>       Prompt securely, or read value from piped stdin
  bb sec get <service> [field]
  bb sec copy [service] [field]
  bb sec env <service>
  bb sec rm <service> [field] [--yes]

Uses the existing age key/store format. Plaintext is never written to disk or journaled.
`)
		return e
	}
	switch args[0] {
	case "init":
		return a.secInit(args[1:])
	case "list", "ls":
		return a.secList(args[1:])
	case "set":
		return a.secSet(args[1:])
	case "get":
		return a.secGet(args[1:])
	case "copy":
		return a.secCopy(args[1:])
	case "env":
		return a.secEnv(args[1:])
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
	if len(args) != 2 || !validSecretName(args[0]) || !validSecretName(args[1]) {
		return usage("sec set", "<service> <field>")
	}
	d, old, e := a.readSecretsSnapshot()
	if e != nil {
		return e
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
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	names := make(map[string]string, len(keys))
	envNames := make(map[string]string, len(keys))
	for _, k := range keys {
		name := secretEnvName(args[0], k)
		if previous, exists := names[name]; exists {
			return invalid(fmt.Sprintf("secret fields %q and %q produce the same environment name %s", previous, k, name))
		}
		names[name] = k
		envNames[k] = name
	}
	for _, k := range keys {
		if _, e := fmt.Fprintf(a.out, "export %s=%s\n", envNames[k], shellQuote(fields[k])); e != nil {
			return e
		}
	}
	return nil
}

func secretEnvName(service, field string) string {
	name := strings.ToUpper(regexpNonEnv.ReplaceAllString(service+"_"+field, "_"))
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
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
		var c []selectChoice
		for s, fs := range d {
			for f := range fs {
				c = append(c, selectChoice{
					Value:       s + "\t" + f,
					Label:       s,
					Description: f,
					SearchText:  s + "/" + f,
				})
			}
		}
		sort.Slice(c, func(i, j int) bool {
			if c[i].Label == c[j].Label {
				return c[i].Description < c[j].Description
			}
			return c[i].Label < c[j].Label
		})
		picked, e := a.selectOne("Secret", c)
		if e != nil {
			return e
		}
		svc, field, _ = strings.Cut(picked, "\t")
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
