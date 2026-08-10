package bb

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var awsProfileNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (a *App) profile(args []string) error {
	if helpRequested(args) || len(args) == 0 {
		_, e := fmt.Fprint(a.out, `Usage:
  bb profile list
  bb profile show <name>
  bb profile add <name> --sso-session NAME --account-id ID --role-name ROLE [--region REGION]
  bb profile edit <name> [same fields]
  bb profile rm <name> [--yes]
  bb profile login <name>

Only ~/.aws/config SSO profiles are managed. credentials and SSO cache remain AWS CLI-owned.
`)
		return e
	}
	switch args[0] {
	case "list":
		return a.profileList(args[1:])
	case "show":
		return a.profileShow(args[1:])
	case "add", "edit":
		return a.profileUpsert(args[0], args[1:])
	case "rm", "remove":
		return a.profileRemove(args[1:])
	case "login":
		if len(args) != 2 || !awsProfileNameRE.MatchString(args[1]) {
			return usage("profile login", "<name>")
		}
		return a.runExternal("aws", "sso", "login", "--profile", args[1])
	default:
		return invalid(fmt.Sprintf("unknown profile command %q", args[0]))
	}
}

func (a *App) awsConfigPath() string {
	if p := a.getenv("AWS_CONFIG_FILE"); p != "" {
		return p
	}
	return filepath.Join(a.getenv("HOME"), ".aws", "config")
}

type iniSection struct {
	name       string
	start, end int
}

func iniSections(lines []string) []iniSection {
	var out []iniSection
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if len(out) > 0 {
				out[len(out)-1].end = i
			}
			out = append(out, iniSection{strings.TrimSpace(t[1 : len(t)-1]), i, len(lines)})
		}
	}
	return out
}
func profileHeader(name string) string {
	if name == "default" {
		return "default"
	}
	return "profile " + name
}
func profileNames(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	var names []string
	for _, s := range iniSections(lines) {
		if s.name == "default" {
			names = append(names, "default")
		} else if strings.HasPrefix(s.name, "profile ") {
			names = append(names, strings.TrimPrefix(s.name, "profile "))
		}
	}
	sort.Strings(names)
	return names
}
func sectionText(data []byte, header string) (string, bool) {
	lines := strings.Split(string(data), "\n")
	for _, s := range iniSections(lines) {
		if s.name == header {
			return strings.Join(lines[s.start:s.end], "\n"), true
		}
	}
	return "", false
}
func sectionFields(data []byte, header string) map[string]string {
	out := map[string]string{}
	text, ok := sectionText(data, header)
	if !ok {
		return out
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func (a *App) readAWSConfig() ([]byte, error) {
	if info, e := os.Lstat(a.awsConfigPath()); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("AWS config must not be a symlink")
	}
	b, e := os.ReadFile(a.awsConfigPath())
	if os.IsNotExist(e) {
		return []byte{}, nil
	}
	return b, e
}
func (a *App) profileList(args []string) error {
	if len(args) != 0 {
		return usage("profile list", "")
	}
	b, e := a.readAWSConfig()
	if e != nil {
		return e
	}
	for _, n := range profileNames(b) {
		fmt.Fprintln(a.out, n)
	}
	return nil
}
func (a *App) profileShow(args []string) error {
	if len(args) != 1 || !awsProfileNameRE.MatchString(args[0]) {
		return usage("profile show", "<name>")
	}
	b, e := a.readAWSConfig()
	if e != nil {
		return e
	}
	text, ok := sectionText(b, profileHeader(args[0]))
	if !ok {
		return invalid("AWS profile not found: " + args[0])
	}
	fmt.Fprintln(a.out, text)
	return nil
}

func parseProfileFields(args []string) (string, map[string]string, error) {
	if len(args) < 1 || !awsProfileNameRE.MatchString(args[0]) {
		return "", nil, usage("profile add|edit", "<name> [--field value...]")
	}
	name := args[0]
	args = args[1:]
	allowed := map[string]string{"--sso-session": "sso_session", "--account-id": "sso_account_id", "--role-name": "sso_role_name", "--region": "region", "--sso-start-url": "sso_start_url", "--sso-region": "sso_region"}
	vals := map[string]string{}
	for len(args) > 0 {
		key, ok := allowed[args[0]]
		if !ok || len(args) < 2 || strings.ContainsAny(args[1], "\x00\r\n") {
			return "", nil, invalid("invalid profile field")
		}
		vals[key] = args[1]
		args = args[2:]
	}
	return name, vals, nil
}

func replaceINISection(data []byte, header string, fields map[string]string, mustExist bool) ([]byte, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	sections := iniSections(lines)
	idx := -1
	var end int
	for _, s := range sections {
		if s.name == header {
			idx = s.start
			end = s.end
			break
		}
	}
	if mustExist && idx < 0 {
		return nil, invalid("AWS profile not found: " + strings.TrimPrefix(header, "profile "))
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	block := []string{"[" + header + "]"}
	for _, k := range keys {
		if fields[k] != "" {
			block = append(block, k+" = "+fields[k])
		}
	}
	if idx < 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
	} else {
		lines = append(append(append([]string{}, lines[:idx]...), block...), lines[end:]...)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func (a *App) backupAWSConfig(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, state, e := a.paths()
	if e != nil {
		return e
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	return writeBytesAtomic(filepath.Join(state, "aws-config-backups", sum+".ini"), data)
}
func (a *App) writeAWSConfig(expected, data []byte) error {
	path := a.awsConfigPath()
	return withFileLock(path, func() error {
		old, e := a.readAWSConfig()
		if e != nil {
			return e
		}
		if sha256.Sum256(old) != sha256.Sum256(expected) {
			return unavailable("AWS config changed during update; retry")
		}
		if e = a.backupAWSConfig(old); e != nil {
			return e
		}
		return writeBytesAtomic(path, data)
	})
}

func (a *App) profileUpsert(mode string, args []string) error {
	name, fields, e := parseProfileFields(args)
	if e != nil {
		return e
	}
	b, e := a.readAWSConfig()
	if e != nil {
		return e
	}
	_, exists := sectionText(b, profileHeader(name))
	if mode == "add" && exists {
		return invalid("AWS profile already exists: " + name)
	}
	if mode == "add" {
		for _, k := range []string{"sso_session", "sso_account_id", "sso_role_name"} {
			if fields[k] == "" {
				return invalid("profile add requires --sso-session, --account-id, and --role-name")
			}
		}
	} else {
		existing := sectionFields(b, profileHeader(name))
		for k, v := range fields {
			existing[k] = v
		}
		fields = existing
	}
	updated, e := replaceINISection(b, profileHeader(name), fields, mode == "edit")
	if e != nil {
		return e
	}
	if e = a.writeAWSConfig(b, updated); e != nil {
		return e
	}
	fmt.Fprintf(a.out, "%s AWS SSO profile %s\n", strings.Title(mode), name)
	return nil
}
func (a *App) profileRemove(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 1 || !awsProfileNameRE.MatchString(args[0]) {
		return usage("profile rm", "<name> [--yes]")
	}
	name := args[0]
	b, e := a.readAWSConfig()
	if e != nil {
		return e
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	idx, end := -1, 0
	for _, s := range iniSections(lines) {
		if s.name == profileHeader(name) {
			idx, end = s.start, s.end
		}
	}
	if idx < 0 {
		return invalid("AWS profile not found: " + name)
	}
	fmt.Fprintln(a.out, "Target AWS profile:", name)
	if !yes {
		ok, e := a.confirmExternal("Remove this profile from ~/.aws/config? [y/N] ")
		if e != nil {
			return e
		}
		if !ok {
			return invalid("profile removal cancelled")
		}
	}
	fresh, e := a.readAWSConfig()
	if e != nil {
		return e
	}
	if fmt.Sprintf("%x", sha256.Sum256(fresh)) != fmt.Sprintf("%x", sha256.Sum256(b)) {
		return unavailable("AWS config changed during confirmation; retry")
	}
	lines = append(lines[:idx], lines[end:]...)
	return a.writeAWSConfig(fresh, []byte(strings.Join(lines, "\n")+"\n"))
}
