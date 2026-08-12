package bb

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

type wenvStore struct {
	Presets map[string]map[string]string `json:"presets"`
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var presetNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const wenvSecretReferencePrefix = "sec://"
const wenvAddPresetAction = "::add-preset::"

const (
	wenvApplyAction          = "apply"
	wenvInspectAction        = "inspect"
	wenvAddVariableAction    = "add-variable"
	wenvUpdateVariableAction = "update-variable"
	wenvRemoveVariableAction = "remove-variable"
	wenvRenamePresetAction   = "rename-preset"
	wenvRemovePresetAction   = "remove-preset"
)

func (a *App) wenvPath() (string, error) {
	c, _, e := a.paths()
	return filepath.Join(c, "wenv.json"), e
}
func (a *App) loadWenv() (wenvStore, string, error) {
	p, e := a.wenvPath()
	s := wenvStore{Presets: map[string]map[string]string{}}
	if e == nil {
		e = readJSON(p, &s)
	}
	if s.Presets == nil {
		s.Presets = map[string]map[string]string{}
	}
	return s, p, e
}
func (a *App) wenv(args []string) error {
	if helpRequested(args) {
		_, e := fmt.Fprint(a.err, `Usage:
  bb wenv                     Open the preset CRUD manager
  bb wenv show <name>         Print the stored, non-secret values without applying
  bb wenv apply [name] [--yes] Preview, confirm, and print eval-safe exports
  bb wenv export [name]       Print eval-safe exports without applying in the shell wrapper
  bb wenv list|current
  bb wenv set <name> KEY=VALUE...
  bb wenv rm <name> [--yes]
  bb wenv import --check|--apply [--dir PATH]

Secret-like variables must use sec://<service>/<field>. References remain
stored and displayed as references, and resolve only for apply/export output.
`)
		return e
	}
	if len(args) == 0 {
		return a.wenvManage()
	}
	switch args[0] {
	case "show":
		return a.wenvShow(args[1:])
	case "apply":
		return a.wenvApply(args[1:])
	case "export":
		return a.wenvExport(args[1:])
	case "list":
		return a.wenvList(args[1:])
	case "current":
		return a.wenvCurrent(args[1:])
	case "set":
		return a.wenvSet(args[1:])
	case "rm":
		return a.wenvRemove(args[1:])
	case "import":
		return a.wenvImport(args[1:])
	default:
		return a.wenvApply(args)
	}
}
func wenvPresetChoices(s wenvStore) []selectChoice {
	names := make([]string, 0, len(s.Presets))
	for n := range s.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	choices := make([]selectChoice, len(names))
	for i, n := range names {
		keys := sortedWenvKeys(s.Presets[n])
		variableWord := "variables"
		if len(keys) == 1 {
			variableWord = "variable"
		}
		choices[i] = selectChoice{
			Value:       n,
			Label:       n,
			Description: fmt.Sprintf("%d %s", len(keys), variableWord),
			SearchText:  strings.Join(keys, " "),
		}
	}
	return choices
}

func (a *App) chooseWenv(s wenvStore) (string, error) {
	choices := wenvPresetChoices(s)
	return a.selectOne("Environment", choices)
}
func (a *App) wenvList(args []string) error {
	if len(args) != 0 {
		return usage("wenv list", "")
	}
	s, _, e := a.loadWenv()
	if e != nil {
		return e
	}
	names := make([]string, 0, len(s.Presets))
	for n := range s.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(a.err, n)
	}
	return nil
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }

func sortedWenvKeys(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a *App) namedWenv(name string) (map[string]string, error) {
	s, _, err := a.loadWenv()
	if err != nil {
		return nil, err
	}
	vars, ok := s.Presets[name]
	if !ok {
		return nil, invalid("wenv preset not found: " + name)
	}
	return vars, nil
}

func (a *App) wenvShow(args []string) error {
	if len(args) != 1 {
		return usage("wenv show", "<name>")
	}
	vars, err := a.namedWenv(args[0])
	if err != nil {
		return err
	}
	return writeWenvValues(a.out, vars)
}

func writeWenvValues(out io.Writer, vars map[string]string) error {
	for _, key := range sortedWenvKeys(vars) {
		if _, err := fmt.Fprintf(out, "%s=%s\n", key, shellQuote(vars[key])); err != nil {
			return err
		}
	}
	return nil
}

func parseWenvSecretReference(value string) (string, string, bool) {
	if !strings.HasPrefix(value, wenvSecretReferencePrefix) {
		return "", "", false
	}
	service, field, ok := strings.Cut(strings.TrimPrefix(value, wenvSecretReferencePrefix), "/")
	if !ok || !validSecretName(service) || !validSecretName(field) || strings.Contains(field, "/") {
		return "", "", false
	}
	return service, field, true
}

func isSecretLikeWenvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, word := range []string{"SECRET", "TOKEN", "PASSWORD", "CREDENTIAL", "PRIVATE_KEY"} {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

func validateWenvValue(key, value string) error {
	if strings.HasPrefix(value, wenvSecretReferencePrefix) {
		if _, _, valid := parseWenvSecretReference(value); !valid {
			return invalid("wenv secret references must use sec://<service>/<field>")
		}
		return nil
	}
	if isSecretLikeWenvKey(key) {
		return invalid("wenv must not store secret-like variables; use sec://<service>/<field>")
	}
	return nil
}

func wenvPreviewValue(key, value string) string {
	if service, field, ok := parseWenvSecretReference(value); ok {
		return fmt.Sprintf("<secret:%s/%s>", service, field)
	}
	if isSecretLikeWenvKey(key) && value != "" {
		return "<redacted>"
	}
	return value
}

func (a *App) resolveWenvValues(vars map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(vars))
	var secrets secretStore
	for key, value := range vars {
		service, field, reference := parseWenvSecretReference(value)
		if !reference {
			resolved[key] = value
			continue
		}
		if secrets == nil {
			var err error
			secrets, err = a.readSecrets()
			if err != nil {
				return nil, fmt.Errorf("resolve wenv secret references: %w", err)
			}
		}
		fields, ok := secrets[service]
		if !ok {
			return nil, invalid("wenv secret service not found: " + service)
		}
		secret, ok := fields[field]
		if !ok {
			return nil, invalid(fmt.Sprintf("wenv secret field not found: %s/%s", service, field))
		}
		resolved[key] = secret
	}
	return resolved, nil
}

func (a *App) wenvApply(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) > 1 {
		return usage("wenv apply", "[name] [--yes]")
	}
	s, _, err := a.loadWenv()
	if err != nil {
		return err
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	} else {
		name, err = a.chooseWenv(s)
		if err != nil || name == "" {
			return err
		}
	}
	vars, ok := s.Presets[name]
	if !ok {
		return invalid("wenv preset not found: " + name)
	}
	fmt.Fprintf(a.err, "Environment %s:\n", name)
	for _, key := range sortedWenvKeys(vars) {
		fmt.Fprintf(a.err, "  %s: %q -> %q\n", key, wenvPreviewValue(key, a.getenv(key)), wenvPreviewValue(key, vars[key]))
	}
	if !yes {
		confirmed, confirmErr := a.confirmAction("Apply this environment?")
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return invalid("wenv apply cancelled")
		}
	}
	resolved, err := a.resolveWenvValues(vars)
	if err != nil {
		return err
	}
	return writeWenvExports(a.out, resolved)
}

func (a *App) wenvExport(args []string) error {
	if len(args) > 1 {
		return usage("wenv export", "[name]")
	}
	s, _, e := a.loadWenv()
	if e != nil {
		return e
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	} else {
		name, e = a.chooseWenv(s)
		if e != nil {
			return e
		}
	}
	if name == "" {
		return nil
	}
	vars, ok := s.Presets[name]
	if !ok {
		return invalid("wenv preset not found: " + name)
	}
	resolved, e := a.resolveWenvValues(vars)
	if e != nil {
		return e
	}
	return writeWenvExports(a.out, resolved)
}

func wenvActionChoices(name string, variableCount int) []selectChoice {
	variableWord := "variables"
	if variableCount == 1 {
		variableWord = "variable"
	}
	return []selectChoice{
		{Value: wenvApplyAction, Label: "Apply", Description: name},
		{Value: wenvInspectAction, Label: "Inspect", Description: fmt.Sprintf("%d %s", variableCount, variableWord)},
		{Value: wenvAddVariableAction, Label: "Add variable", Description: name},
		{Value: wenvUpdateVariableAction, Label: "Update variable", Description: name},
		{Value: wenvRemoveVariableAction, Label: "Remove variable", Description: name},
		{Value: wenvRenamePresetAction, Label: "Rename preset", Description: name},
		{Value: wenvRemovePresetAction, Label: "Remove preset", Description: name},
	}
}

func wenvVariableChoices(name string, vars map[string]string) []selectChoice {
	keys := sortedWenvKeys(vars)
	choices := make([]selectChoice, len(keys))
	for i, key := range keys {
		description := "stored value"
		if _, _, secret := parseWenvSecretReference(vars[key]); secret {
			description = "secret reference"
		}
		choices[i] = selectChoice{Value: key, Label: key, Description: description, SearchText: name}
	}
	return choices
}

func (a *App) wenvManage() error {
	store, _, err := a.loadWenv()
	if err != nil {
		return err
	}
	presets := wenvPresetChoices(store)
	presets = append(presets, selectChoice{
		Value:       wenvAddPresetAction,
		Label:       "Add preset",
		Description: "Create environment variables",
	})
	root := selectStage{Prompt: "Environment", Choices: presets}
	next := func(path []string) *selectStage {
		switch len(path) {
		case 1:
			name := path[0]
			if name == wenvAddPresetAction {
				return nil
			}
			return &selectStage{Prompt: "Action for " + name, Choices: wenvActionChoices(name, len(store.Presets[name]))}
		case 2:
			name, action := path[0], path[1]
			if action == wenvUpdateVariableAction || action == wenvRemoveVariableAction {
				return &selectStage{Prompt: "Variable in " + name, Choices: wenvVariableChoices(name, store.Presets[name])}
			}
			return nil
		default:
			return nil
		}
	}
	outcome, err := a.selectStages(root, next)
	if err != nil {
		return err
	}
	if outcome.Cancelled || len(outcome.Path) == 0 {
		return nil
	}
	if outcome.Path[0] == wenvAddPresetAction {
		return a.promptWenvAdd(store)
	}
	if len(outcome.Path) < 2 {
		return nil
	}
	name, action := outcome.Path[0], outcome.Path[1]
	switch action {
	case wenvApplyAction:
		return a.wenvApply([]string{name})
	case wenvInspectAction:
		if _, err := fmt.Fprintf(a.err, "Environment %s:\n", name); err != nil {
			return err
		}
		return writeWenvValues(a.err, store.Presets[name])
	case wenvAddVariableAction:
		assignment, ok, promptErr := a.promptWenvAssignment()
		if promptErr != nil || !ok {
			return promptErr
		}
		return a.wenvAddVariable(name, assignment)
	case wenvUpdateVariableAction:
		if len(outcome.Path) < 3 {
			return nil
		}
		value, ok, promptErr := a.promptWenvValue(outcome.Path[2])
		if promptErr != nil || !ok {
			return promptErr
		}
		return a.wenvUpdateVariable(name, outcome.Path[2], value)
	case wenvRemoveVariableAction:
		if len(outcome.Path) < 3 {
			return nil
		}
		return a.wenvRemoveVariable(name, outcome.Path[2])
	case wenvRenamePresetAction:
		return a.promptWenvRename(name)
	case wenvRemovePresetAction:
		return a.wenvRemove([]string{name})
	default:
		return invalid("unknown wenv action")
	}
}

func (a *App) promptWenvAdd(store wenvStore) error {
	if _, err := fmt.Fprint(a.err, "Preset name [Enter=cancel]: "); err != nil {
		return err
	}
	name, err := readLine(a.in)
	name = strings.TrimSpace(name)
	if err != nil && name == "" {
		return fmt.Errorf("read wenv preset name: %w", err)
	}
	if name == "" {
		return nil
	}
	if !presetNameRE.MatchString(name) {
		return invalid("wenv preset names may contain only letters, digits, dot, underscore, and hyphen")
	}
	if _, exists := store.Presets[name]; exists {
		return invalid("wenv preset already exists: " + name)
	}

	args := []string{name}
	for {
		if _, err := fmt.Fprint(a.err, "Variable KEY=VALUE [Enter=done]: "); err != nil {
			return err
		}
		value, readErr := readLine(a.in)
		value = strings.TrimSpace(value)
		if readErr != nil && value == "" {
			return fmt.Errorf("read wenv variable: %w", readErr)
		}
		if value == "" {
			break
		}
		args = append(args, value)
	}
	if len(args) == 1 {
		return invalid("wenv preset requires at least one KEY=VALUE variable")
	}
	return a.wenvSet(args)
}

func (a *App) promptWenvAssignment() (string, bool, error) {
	if _, err := fmt.Fprint(a.err, "Variable KEY=VALUE [Enter=cancel]: "); err != nil {
		return "", false, err
	}
	assignment, err := readLine(a.in)
	assignment = strings.TrimSpace(assignment)
	if err != nil && assignment == "" {
		return "", false, fmt.Errorf("read wenv variable: %w", err)
	}
	return assignment, assignment != "", nil
}

func (a *App) promptWenvValue(key string) (string, bool, error) {
	if _, err := fmt.Fprintf(a.err, "New value for %s [Enter=cancel]: ", key); err != nil {
		return "", false, err
	}
	value, err := readLine(a.in)
	value = strings.TrimSpace(value)
	if err != nil && value == "" {
		return "", false, fmt.Errorf("read wenv value: %w", err)
	}
	return value, value != "", nil
}

func parseWenvAssignment(assignment string) (string, string, error) {
	key, value, ok := strings.Cut(assignment, "=")
	if !ok || !envKeyRE.MatchString(key) {
		return "", "", invalid("wenv values must use KEY=VALUE")
	}
	if err := validateWenvValue(key, value); err != nil {
		return "", "", err
	}
	return key, value, nil
}

func (a *App) wenvAddVariable(name, assignment string) error {
	key, value, err := parseWenvAssignment(assignment)
	if err != nil {
		return err
	}
	store, path, err := a.loadWenv()
	if err != nil {
		return err
	}
	vars, exists := store.Presets[name]
	if !exists {
		return invalid("wenv preset not found: " + name)
	}
	if _, exists := vars[key]; exists {
		return invalid("wenv variable already exists: " + key)
	}
	vars[key] = value
	return writeJSON(path, store)
}

func (a *App) wenvUpdateVariable(name, key, value string) error {
	if err := validateWenvValue(key, value); err != nil {
		return err
	}
	store, path, err := a.loadWenv()
	if err != nil {
		return err
	}
	vars, exists := store.Presets[name]
	if !exists {
		return invalid("wenv preset not found: " + name)
	}
	if _, exists := vars[key]; !exists {
		return invalid("wenv variable not found: " + key)
	}
	vars[key] = value
	return writeJSON(path, store)
}

func (a *App) wenvRemoveVariable(name, key string) error {
	store, path, err := a.loadWenv()
	if err != nil {
		return err
	}
	vars, exists := store.Presets[name]
	if !exists {
		return invalid("wenv preset not found: " + name)
	}
	if _, exists := vars[key]; !exists {
		return invalid("wenv variable not found: " + key)
	}
	if len(vars) == 1 {
		return invalid("cannot remove the last wenv variable; remove the preset instead")
	}
	confirmed, err := a.confirmAction("Remove wenv variable " + name + "/" + key + "?")
	if err != nil {
		return err
	}
	if !confirmed {
		return invalid("wenv variable removal cancelled")
	}
	delete(vars, key)
	return writeJSON(path, store)
}

func (a *App) promptWenvRename(oldName string) error {
	if _, err := fmt.Fprint(a.err, "New preset name [Enter=cancel]: "); err != nil {
		return err
	}
	newName, err := readLine(a.in)
	newName = strings.TrimSpace(newName)
	if err != nil && newName == "" {
		return fmt.Errorf("read wenv preset name: %w", err)
	}
	if newName == "" {
		return nil
	}
	if !presetNameRE.MatchString(newName) {
		return invalid("wenv preset names may contain only letters, digits, dot, underscore, and hyphen")
	}
	if newName == oldName {
		return invalid("new wenv preset name must differ from the current name")
	}
	store, path, err := a.loadWenv()
	if err != nil {
		return err
	}
	vars, exists := store.Presets[oldName]
	if !exists {
		return invalid("wenv preset not found: " + oldName)
	}
	if _, exists := store.Presets[newName]; exists {
		return invalid("target wenv preset already exists: " + newName)
	}
	confirmed, err := a.confirmAction("Rename wenv preset " + oldName + " to " + newName + "?")
	if err != nil {
		return err
	}
	if !confirmed {
		return invalid("wenv preset rename cancelled")
	}
	store.Presets[newName] = vars
	delete(store.Presets, oldName)
	return writeJSON(path, store)
}

func writeWenvExports(out io.Writer, vars map[string]string) error {
	for _, key := range sortedWenvKeys(vars) {
		if _, err := fmt.Fprintf(out, "export %s=%s\n", key, shellQuote(vars[key])); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) wenvSet(args []string) error {
	if len(args) < 2 || !presetNameRE.MatchString(args[0]) {
		return usage("wenv set", "<name> KEY=VALUE...")
	}
	s, p, e := a.loadWenv()
	if e != nil {
		return e
	}
	vars := map[string]string{}
	for _, kv := range args[1:] {
		k, v, parseErr := parseWenvAssignment(kv)
		if parseErr != nil {
			return parseErr
		}
		vars[k] = v
	}
	s.Presets[args[0]] = vars
	return writeJSON(p, s)
}
func (a *App) wenvRemove(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 1 {
		return usage("wenv rm", "<name> [--yes]")
	}
	s, p, e := a.loadWenv()
	if e != nil {
		return e
	}
	if _, ok := s.Presets[args[0]]; !ok {
		return invalid("wenv preset not found: " + args[0])
	}
	if !yes {
		ok, e := a.confirmAction("Remove wenv preset?")
		if e != nil {
			return e
		}
		if !ok {
			return invalid("wenv removal cancelled")
		}
	}
	delete(s.Presets, args[0])
	return writeJSON(p, s)
}
func (a *App) wenvCurrent(args []string) error {
	if len(args) != 0 {
		return usage("wenv current", "")
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_REGION", "KUBE_CONTEXT", "KUBE_NAMESPACE"} {
		fmt.Fprintf(a.err, "%s=%s\n", k, a.getenv(k))
	}
	return nil
}

func parseLegacyWenv(path string) (map[string]string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	vars := map[string]string{}
	inExports := false
	parseExports := func(value string) error {
		words, splitErr := splitLegacyWords(value)
		if splitErr != nil {
			return splitErr
		}
		for _, kv := range words {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !envKeyRE.MatchString(k) {
				return invalid("unsupported legacy EXPORTS entry")
			}
			v = strings.Trim(v, "'\"")
			if e := validateWenvValue(k, v); e != nil {
				return e
			}
			vars[k] = v
		}
		return nil
	}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if inExports {
			if line == ")" {
				inExports = false
				continue
			}
			if e := parseExports(line); e != nil {
				return nil, e
			}
			continue
		}
		if line == "EXPORTS=(" {
			inExports = true
			continue
		}
		if strings.HasPrefix(line, "EXPORTS=(") && strings.HasSuffix(line, ")") {
			if e := parseExports(strings.TrimSuffix(strings.TrimPrefix(line, "EXPORTS=("), ")")); e != nil {
				return nil, e
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !envKeyRE.MatchString(k) || strings.ContainsAny(v, "`$();") {
			return nil, invalid("legacy wenv contains executable or unsupported syntax: " + filepath.Base(path))
		}
		v = strings.Trim(v, "'\"")
		if e := validateWenvValue(k, v); e != nil {
			return nil, e
		}
		vars[k] = v
	}
	if inExports {
		return nil, invalid("legacy wenv contains an unterminated EXPORTS array: " + filepath.Base(path))
	}
	return vars, scan.Err()
}

func splitLegacyWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		if strings.ContainsRune("`$();", r) {
			return nil, invalid("legacy wenv contains executable syntax")
		}
		b.WriteRune(r)
	}
	if quote != 0 || escaped {
		return nil, invalid("legacy wenv contains invalid quoting")
	}
	flush()
	return out, nil
}
func (a *App) wenvImport(args []string) error {
	apply := false
	check := false
	dir := filepath.Join(a.getenv("HOME"), ".config", "binbox", "wenv.d")
	for len(args) > 0 {
		switch args[0] {
		case "--check":
			check = true
			args = args[1:]
		case "--apply":
			apply = true
			args = args[1:]
		case "--dir":
			if len(args) < 2 {
				return usage("wenv import", "--check|--apply [--dir PATH]")
			}
			dir, args = args[1], args[2:]
		default:
			return usage("wenv import", "--check|--apply [--dir PATH]")
		}
	}
	if check == apply {
		return invalid("choose exactly one of --check or --apply")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	s, p, e := a.loadWenv()
	if e != nil {
		return e
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && presetNameRE.MatchString(entry.Name()) {
			vars, e := parseLegacyWenv(filepath.Join(dir, entry.Name()))
			if e != nil {
				return e
			}
			fmt.Fprintln(a.err, entry.Name())
			if apply {
				if existing, ok := s.Presets[entry.Name()]; ok && !reflect.DeepEqual(existing, vars) {
					return invalid("wenv import collision: " + entry.Name())
				}
				s.Presets[entry.Name()] = vars
			}
			count++
		}
	}
	if apply {
		return writeJSON(p, s)
	}
	fmt.Fprintf(a.err, "%d importable preset(s)\n", count)
	return nil
}
