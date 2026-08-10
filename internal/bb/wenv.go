package bb

import (
	"bufio"
	"fmt"
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
  bb wenv export [name]       Print eval-safe exports; select by number when omitted
  bb wenv list|current
  bb wenv set <name> KEY=VALUE...
  bb wenv rm <name> [--yes]
	bb wenv import --check|--apply [--dir PATH]
`)
		return e
	}
	if len(args) == 0 {
		return a.wenvExport(nil)
	}
	switch args[0] {
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
		return a.wenvExport(args)
	}
}
func (a *App) chooseWenv(s wenvStore) (string, error) {
	names := make([]string, 0, len(s.Presets))
	for n := range s.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	choices := make([]selectChoice, len(names))
	for i, n := range names {
		choices[i] = selectChoice{n, n}
	}
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
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(a.out, "export %s=%s\n", k, shellQuote(vars[k]))
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
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !envKeyRE.MatchString(k) {
			return invalid("wenv values must use KEY=VALUE")
		}
		upper := strings.ToUpper(k)
		for _, word := range []string{"SECRET", "TOKEN", "PASSWORD", "CREDENTIAL", "PRIVATE_KEY"} {
			if strings.Contains(upper, word) {
				return invalid("wenv must not store secret-like variables; use bb sec")
			}
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
		ok, e := a.confirmExternal("Remove wenv preset? [y/N] ")
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
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "EXPORTS=(") && strings.HasSuffix(line, ")") {
			words, splitErr := splitLegacyWords(strings.TrimSuffix(strings.TrimPrefix(line, "EXPORTS=("), ")"))
			if splitErr != nil {
				return nil, splitErr
			}
			for _, kv := range words {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || !envKeyRE.MatchString(k) {
					return nil, invalid("unsupported legacy EXPORTS entry")
				}
				vars[k] = strings.Trim(v, "'\"")
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !envKeyRE.MatchString(k) || strings.ContainsAny(v, "`$();") {
			return nil, invalid("legacy wenv contains executable or unsupported syntax: " + filepath.Base(path))
		}
		vars[k] = strings.Trim(v, "'\"")
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
