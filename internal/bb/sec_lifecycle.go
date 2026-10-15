package bb

// Lifecycle helpers around a stored value: where it is referenced, when it was
// last used, importing a plaintext .env once, and rotating with a way back.
// None of them print a value.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// markSecretsUsed records today as the last use of keys. It writes at most once
// a day per key and never fails the command that used the value.
func (a *App) markSecretsUsed(keys []string) {
	if len(keys) == 0 {
		return
	}
	today := a.secretToday().Format(secretDateLayout)
	meta, err := a.readSecretMeta()
	if err != nil {
		return
	}
	stale := false
	for _, key := range keys {
		if meta[key].LastUsedAt != today {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	// A held lock skips the record instead of stalling the command using the
	// value.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	path := a.secMetaPath()
	_ = withFileLockContext(ctx, path, func() error {
		entries, err := a.readSecretMeta()
		if err != nil {
			return err
		}
		for _, key := range keys {
			entry := entries[key]
			entry.LastUsedAt = today
			entries[key] = entry
		}
		return writeJSONAtomic(path, secretMetaFile{SchemaVersion: 1, Entries: entries})
	})
}

func serviceKeys(service string, fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for field := range fields {
		keys = append(keys, secretKey(service, field))
	}
	return keys
}

// secretReference is one place bb configuration points at a secret.
type secretReference struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Key    string `json:"key"`
}

// secretReferences lists wenv presets, MCP servers and MCP exec bindings that
// use service (and field, when given). It reads configuration only.
func (a *App) secretReferences(service, field string) ([]secretReference, error) {
	matches := func(reference string) (string, bool) {
		s, f, ok := parseWenvSecretReference(reference)
		if !ok || s != service || (field != "" && f != field) {
			return "", false
		}
		return secretKey(s, f), true
	}
	refs := []secretReference{}
	wenv, _, err := a.loadWenv()
	if err != nil {
		return nil, err
	}
	for preset, vars := range wenv.Presets {
		for variable, value := range vars {
			if key, ok := matches(value); ok {
				refs = append(refs, secretReference{Kind: "wenv", Name: preset, Detail: variable, Key: key})
			}
		}
	}
	mcp, _, err := a.loadMCP()
	if err != nil {
		return nil, err
	}
	for name, server := range mcp.Servers {
		for variable, value := range server.SecretEnv {
			if key, ok := matches(value); ok {
				refs = append(refs, secretReference{Kind: "mcp", Name: name, Detail: variable, Key: key})
			}
		}
		if key, ok := matches(server.BearerTokenSecret); ok {
			refs = append(refs, secretReference{Kind: "mcp", Name: name, Detail: "bearer token", Key: key})
		}
	}
	serve, err := a.loadMCPServeConfig()
	if err != nil {
		return nil, err
	}
	// Exec bindings name whole services, so they matter for a field too.
	for _, rule := range serve.ExecRules {
		if containsString(rule.Services, service) {
			refs = append(refs, secretReference{Kind: "mcp-allow", Name: rule.String(), Detail: "service binding", Key: service})
		}
	}
	if containsString(serve.ExecServices, service) {
		refs = append(refs, secretReference{Kind: "mcp-allow", Name: "service scope", Detail: "exec scope", Key: service})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].Detail < refs[j].Detail
	})
	return refs, nil
}

func (a *App) secWhere(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) < 1 || len(args) > 2 || !validSecretName(args[0]) || (len(args) == 2 && !validSecretName(args[1])) {
		return usage("sec where", "<service[/field]> [--json]")
	}
	field := ""
	if len(args) == 2 {
		field = args[1]
	}
	refs, err := a.secretReferences(args[0], field)
	if err != nil {
		return err
	}
	if jsonMode {
		return printEnvelope(a.out, refs, nil)
	}
	if len(refs) == 0 {
		_, err := fmt.Fprintln(a.err, "No wenv preset, MCP server or MCP exec rule references "+strings.Join(args, "/"))
		return err
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, ref := range refs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ref.Kind, safeTerminalText(ref.Name), safeTerminalText(ref.Detail), safeTerminalText(ref.Key))
	}
	return w.Flush()
}

// warnSecretReferences tells the operator that configuration still points at
// keys that were just removed or renamed. It never blocks the change.
func (a *App) warnSecretReferences(service, field, action string) {
	refs, err := a.secretReferences(service, field)
	if err != nil || len(refs) == 0 {
		return
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Kind+" "+ref.Name+" ("+ref.Detail+")")
	}
	target := service
	if field != "" {
		target = secretKey(service, field)
	}
	fmt.Fprintf(a.err, "Note: %s is %s but still referenced by: %s\n", target, action, safeTerminalText(strings.Join(sortedUnique(names), ", ")))
}

// parseDotenv reads KEY=VALUE lines. It accepts "export KEY=..." and quoted
// values: single quotes are literal, double quotes understand \n, \", \\.
func parseDotenv(r io.Reader) (map[string]string, []string, error) {
	values := map[string]string{}
	order := []string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSecretValueBytes)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyRE.MatchString(key) {
			return nil, nil, invalid(fmt.Sprintf("line %d is not KEY=VALUE", line))
		}
		value, err := dotenvValue(strings.TrimSpace(value))
		if err != nil {
			return nil, nil, invalid(fmt.Sprintf("line %d: %s", line, err.Error()))
		}
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read .env: %w", err)
	}
	return values, order, nil
}

// dotenvValue unquotes one value. A quoted value must close, and only a
// comment may follow it; double quotes understand \n, \t, \", \\ and \$.
func dotenvValue(raw string) (string, error) {
	if raw == "" || (raw[0] != '\'' && raw[0] != '"') {
		// An unquoted value ends at an inline comment.
		if index := strings.Index(raw, " #"); index >= 0 {
			raw = strings.TrimSpace(raw[:index])
		}
		return raw, nil
	}
	quote := raw[0]
	var value strings.Builder
	for index := 1; index < len(raw); index++ {
		c := raw[index]
		if c == quote {
			rest := strings.TrimSpace(raw[index+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", fmt.Errorf("unexpected text after the closing quote")
			}
			return value.String(), nil
		}
		if quote == '"' && c == '\\' && index+1 < len(raw) {
			index++
			switch raw[index] {
			case 'n':
				value.WriteByte('\n')
			case 't':
				value.WriteByte('\t')
			case '"', '\\', '$':
				value.WriteByte(raw[index])
			default:
				value.WriteByte('\\')
				value.WriteByte(raw[index])
			}
			continue
		}
		value.WriteByte(c)
	}
	return "", fmt.Errorf("the quoted value does not close")
}

// secImport stores a .env file as fields of one service, keeping each variable
// name as the field name.
func (a *App) secImport(args []string) error {
	args, force := takeFlag(args, "--force")
	args, service, hasService, err := takeOptionValue(args, "--service")
	if err != nil {
		return err
	}
	args, only, hasOnly, err := takeOptionValue(args, "--only")
	if err != nil {
		return err
	}
	if len(args) != 1 || !hasService || !validSecretName(service) {
		return usage("sec import", "<file.env|-> --service <service> [--only KEY,KEY] [--force]")
	}
	var input io.Reader = a.in
	if args[0] != "-" {
		file, err := os.Open(args[0])
		if err != nil {
			return fmt.Errorf("open %s: %w", args[0], err)
		}
		defer file.Close()
		input = file
	}
	values, order, err := parseDotenv(input)
	if err != nil {
		return err
	}
	if hasOnly {
		wanted := map[string]bool{}
		for _, key := range strings.Split(only, ",") {
			wanted[strings.TrimSpace(key)] = true
		}
		kept := order[:0]
		for _, key := range order {
			if wanted[key] {
				kept = append(kept, key)
				delete(wanted, key)
			}
		}
		if len(wanted) > 0 {
			missing := make([]string, 0, len(wanted))
			for key := range wanted {
				missing = append(missing, key)
			}
			sort.Strings(missing)
			return invalid("not in the file: " + strings.Join(missing, ", "))
		}
		order = kept
	}
	imported := make([]string, 0, len(order))
	for _, key := range order {
		if values[key] != "" {
			imported = append(imported, key)
		}
	}
	if len(imported) == 0 {
		return invalid("no non-empty KEY=VALUE entries to import")
	}
	data, old, err := a.readSecretsSnapshot()
	if err != nil {
		return err
	}
	if !force {
		conflicts := []string{}
		for _, key := range imported {
			if _, exists := data[service][key]; exists {
				conflicts = append(conflicts, key)
			}
		}
		if len(conflicts) > 0 {
			return invalid(fmt.Sprintf("%s already has %s; use --force to replace", service, strings.Join(conflicts, ", ")))
		}
	}
	// Values --force replaces stay restorable like a rotation.
	meta, err := a.readSecretMeta()
	if err != nil {
		return err
	}
	replaced := []secretTrashEntry{}
	for _, key := range imported {
		if previous, exists := data[service][key]; exists && previous != values[key] {
			entry := secretTrashEntry{Service: service, Field: key, Value: previous, DeletedAt: a.now().UTC(), Rotated: true}
			if m, ok := meta[secretKey(service, key)]; ok {
				entry.Meta = &m
			}
			replaced = append(replaced, entry)
		}
	}
	if len(replaced) > 0 {
		if err := a.moveSecretsToTrash(replaced); err != nil {
			return fmt.Errorf("keep the replaced values (nothing imported): %w", err)
		}
	}
	if data[service] == nil {
		data[service] = map[string]string{}
	}
	for _, key := range imported {
		data[service][key] = values[key]
	}
	if err := a.writeSecrets(data, old); err != nil {
		return err
	}
	if err := a.updateSecretMeta(func(entries map[string]secretMeta) error {
		now := a.now().UTC().Format(time.RFC3339)
		for _, entry := range replaced {
			// The recorded expiry belonged to the replaced value.
			current := entries[secretKey(service, entry.Field)]
			current.ExpiresAt, current.ExpiresSource = "", ""
			entries[secretKey(service, entry.Field)] = current
		}
		for _, key := range imported {
			if date, ok := jwtExpiry(values[key]); ok {
				entry := entries[secretKey(service, key)]
				entry.ExpiresAt, entry.ExpiresSource, entry.UpdatedAt = date, "jwt", now
				entries[secretKey(service, key)] = entry
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintln(a.err, "Imported, but could not record JWT expiry: "+err.Error())
	}
	fmt.Fprintf(a.err, "Imported %d field(s) into %s: %s\n", len(imported), service, strings.Join(imported, ", "))
	for _, entry := range replaced {
		fmt.Fprintf(a.err, "Replaced %s; the previous value is kept for 7 days (bb sec rotate %s --undo).\n", secretKey(service, entry.Field), secretKey(service, entry.Field))
	}
	if args[0] != "-" {
		fmt.Fprintf(a.err, "The plaintext file is unchanged; delete it once nothing reads it: rm %s\n", shellQuote(args[0]))
	}
	return nil
}

// secRotate replaces a value and keeps the previous one in the trash marked
// as rotated, so --undo can swap them back within the retention window.
func (a *App) secRotate(args []string) error {
	args, undo := takeFlag(args, "--undo")
	args, paste := takeFlag(args, "--paste")
	args, opts, err := takeSecretMetaOptions(args)
	if err != nil {
		return err
	}
	if len(args) != 2 || !validSecretName(args[0]) || !validSecretName(args[1]) {
		return usage("sec rotate", "<service/field> [--paste] [--expires DATE|90d] [--url URL] [--note TEXT] | <service/field> --undo")
	}
	service, field := args[0], args[1]
	key := secretKey(service, field)
	if undo {
		if paste || opts.any() {
			return invalid("--undo takes no value or metadata options")
		}
		return a.secRotateUndo(service, field)
	}
	if _, err := opts.apply(secretMeta{}, a.secretToday(), ""); err != nil {
		return err
	}
	data, _, err := a.readSecretsSnapshot()
	if err != nil {
		return err
	}
	previous, exists := data[service][field]
	if !exists {
		return invalid("secret field not found; add it with: bb sec add " + key)
	}
	var value []byte
	pastedDigest := ""
	if paste {
		value, err = a.readClipboard()
		value = bytes.TrimRight(value, "\r\n")
		pastedDigest = clipboardDigest(value)
	} else {
		value, err = a.readSecretValue()
	}
	if err != nil {
		return err
	}
	if len(value) == 0 {
		return invalid("secret value must not be empty")
	}
	if string(value) == previous {
		return invalid("the new value is the same as the current one")
	}
	// Re-read before touching the trash: the snapshot above may be stale after
	// the prompt, and the write below is conditional on this one.
	data, old, err := a.readSecretsSnapshot()
	if err != nil {
		return err
	}
	if data[service][field] != previous {
		return invalid("the field changed while rotating; nothing was replaced")
	}
	meta, err := a.readSecretMeta()
	if err != nil {
		return err
	}
	entry := secretTrashEntry{Service: service, Field: field, Value: previous, DeletedAt: a.now().UTC(), Rotated: true}
	if m, ok := meta[key]; ok {
		entry.Meta = &m
	}
	if err := a.moveSecretsToTrash([]secretTrashEntry{entry}); err != nil {
		return fmt.Errorf("keep the previous value (nothing changed): %w", err)
	}
	data[service][field] = string(value)
	if err := a.writeSecrets(data, old); err != nil {
		return err
	}
	if paste {
		if err := a.clearClipboardIfUnchanged(pastedDigest); err != nil {
			fmt.Fprintln(a.err, "Could not clear the clipboard: "+err.Error())
		}
	}
	jwtDate, _ := jwtExpiry(string(value))
	err = a.updateSecretMeta(func(entries map[string]secretMeta) error {
		current := entries[key]
		// The recorded expiry belonged to the old value.
		current.ExpiresAt, current.ExpiresSource = "", ""
		updated, applyErr := opts.apply(current, a.secretToday(), jwtDate)
		if applyErr != nil {
			return applyErr
		}
		updated.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		entries[key] = updated
		return nil
	})
	if err != nil {
		return fmt.Errorf("rotated %s, but could not update its metadata: %w", key, err)
	}
	fmt.Fprintf(a.err, "Rotated %s; the previous value is kept for 7 days (bb sec rotate %s --undo).\n", key, key)
	if meta[key].Verify != nil {
		if results, verifyErr := a.runSecretVerification(service, field); verifyErr == nil && len(results) == 1 {
			result := results[0]
			if !result.OK {
				fmt.Fprintf(a.err, "The new value failed verification (%s); undo with: bb sec rotate %s --undo\n", safeTerminalText(result.Error), key)
			} else {
				fmt.Fprintf(a.err, "Verified as %s.\n", safeTerminalText(result.Identity))
				if result.ExpiresAt != "" {
					fmt.Fprintf(a.err, "Expiry %s recorded from %s.\n", result.ExpiresAt, result.Kind)
					return nil
				}
			}
		}
	}
	if !opts.hasExpires && jwtDate == "" {
		fmt.Fprintf(a.err, "No expiry recorded for the new value; set it with: bb sec meta %s --expires <date|90d>\n", key)
	}
	return nil
}

// secRotateUndo swaps the current value with the newest rotated copy.
func (a *App) secRotateUndo(service, field string) error {
	key := secretKey(service, field)
	trash, oldTrash, err := a.readSecretTrash()
	if err != nil {
		return err
	}
	a.pruneSecretTrash(&trash)
	picked := -1
	for index, entry := range trash.Entries {
		if entry.Rotated && entry.Service == service && entry.Field == field &&
			(picked < 0 || entry.DeletedAt.After(trash.Entries[picked].DeletedAt)) {
			picked = index
		}
	}
	if picked < 0 {
		return invalid("no rotated value of " + key + " in the trash")
	}
	data, oldStore, err := a.readSecretsSnapshot()
	if err != nil {
		return err
	}
	current, exists := data[service][field]
	if !exists {
		return invalid("secret field not found; restore it with: bb sec restore " + key)
	}
	meta, err := a.readSecretMeta()
	if err != nil {
		return err
	}
	previous := trash.Entries[picked]
	swapped := secretTrashEntry{Service: service, Field: field, Value: current, DeletedAt: a.now().UTC(), Rotated: true}
	if m, ok := meta[key]; ok {
		swapped.Meta = &m
	}
	// Both values stay in the trash until the store holds the restored one,
	// so a failed store write loses neither.
	trash.Entries = append(trash.Entries, swapped)
	if err := a.writeEncryptedJSON(a.secTrashPath(), trash, oldTrash, false); err != nil {
		return fmt.Errorf("update the trash (nothing changed): %w", err)
	}
	data[service][field] = previous.Value
	if err := a.writeSecrets(data, oldStore); err != nil {
		return err
	}
	if err := a.dropTrashEntry(previous); err != nil {
		fmt.Fprintln(a.err, "Restored, but the restored copy is still in the trash: "+err.Error())
	}
	if err := a.updateSecretMeta(func(entries map[string]secretMeta) error {
		restored := secretMeta{}
		if previous.Meta != nil {
			restored = *previous.Meta
		}
		restored.LastUsedAt = entries[key].LastUsedAt
		entries[key] = restored
		return nil
	}); err != nil {
		return fmt.Errorf("restored the previous value of %s, but not its metadata: %w", key, err)
	}
	fmt.Fprintf(a.err, "Restored the previous value of %s; the replaced one is in the trash (run --undo again to swap back).\n", key)
	return nil
}

type secretUsage struct {
	Key        string `json:"key"`
	LastUsedAt string `json:"last_used_at"`
	DaysAgo    int    `json:"days_ago"`
}

// secretUnusedReport lists recorded uses older than days. Fields used before
// usage tracking existed have no record and are not listed.
func (a *App) secretUnusedReport(days int) ([]secretUsage, error) {
	meta, err := a.readSecretMeta()
	if err != nil {
		return nil, err
	}
	today := a.secretToday()
	report := []secretUsage{}
	for key, entry := range meta {
		if entry.LastUsedAt == "" {
			continue
		}
		used, err := time.Parse(secretDateLayout, entry.LastUsedAt)
		if err != nil {
			continue
		}
		ago := int(today.Sub(used).Hours() / 24)
		if ago >= days {
			report = append(report, secretUsage{Key: key, LastUsedAt: entry.LastUsedAt, DaysAgo: ago})
		}
	}
	sort.Slice(report, func(i, j int) bool { return report[i].DaysAgo > report[j].DaysAgo })
	return report, nil
}

func (a *App) printUnusedSecrets(days int, jsonMode bool) error {
	report, err := a.secretUnusedReport(days)
	if err != nil {
		return err
	}
	if jsonMode {
		return printEnvelope(a.out, report, nil)
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, usage := range report {
		fmt.Fprintf(w, "%s\tlast used %s\t%dd ago\n", safeTerminalText(usage.Key), usage.LastUsedAt, usage.DaysAgo)
	}
	return w.Flush()
}
