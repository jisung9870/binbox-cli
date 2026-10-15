package bb

// Plaintext-token detection in MCP client configuration and adoption of such a
// server into bb: its values move into bb sec and the client is re-registered
// through "bb mcp launch". Findings name a location, never a value.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
)

// clientMCPServer is one server as a client's own configuration describes it.
// Unmodeled lists keys bb does not carry over, which adopt must not drop
// silently.
type clientMCPServer struct {
	Client    string
	Scope     string
	Path      string
	Name      string
	Type      string
	Command   string
	Args      []string
	URL       string
	Env       map[string]string
	Headers   map[string]string
	Unmodeled []string
}

// unreadableConfig is a client file bb could not parse; audit reports it
// instead of calling the configuration clean.
type unreadableConfig struct {
	Client, Path string
}

var (
	tokenValueRE  = regexp.MustCompile(`(^|[^A-Za-z0-9])(glpat-|gldt-|glrt-|glsa_|ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|xox[abpre]-|sk-[A-Za-z0-9]|sk_live_|rk_live_|AKIA[0-9A-Z]{12}|ASIA[0-9A-Z]{12}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.)`)
	secretFlagRE  = regexp.MustCompile(`(?i)^--?[a-z0-9_-]*(token|api-?key|apikey|secret|password|passwd|bearer|credential|private-?key|pat)$`)
	nonAlnumRE    = regexp.MustCompile(`[^A-Za-z0-9]`)
	literalAuthRE = regexp.MustCompile(`(?i)^\s*(proxy-)?authorization\s*:\s*\S+\s+\S`)
)

// secretLikeName normalizes separators so X-API-Key, api_key and APIKEY all
// count. Short words (PAT, AUTH) must be a whole segment so PATH and AUTHOR do
// not.
func secretLikeName(name string) bool {
	normalized := strings.ToUpper(nonAlnumRE.ReplaceAllString(name, ""))
	for _, word := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "APIKEY", "COOKIE", "CREDENTIAL", "PRIVATEKEY"} {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	for _, segment := range nonAlnumRE.Split(strings.ToUpper(name), -1) {
		if segment == "PAT" || segment == "AUTH" || segment == "AUTHORIZATION" {
			return true
		}
	}
	return false
}

// envReference reports whether a value only points at the environment, which
// is how a client config should carry a token.
func envReference(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") && !strings.Contains(trimmed[2:], "${")
}

func plaintextSecret(key, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || envReference(value) || strings.HasPrefix(value, wenvSecretReferencePrefix) {
		return "", false
	}
	if tokenValueRE.MatchString(value) {
		return "token-shaped value", true
	}
	if secretLikeName(key) {
		return "secret-like name with a literal value", true
	}
	return "", false
}

type mcpSecretFinding struct {
	Client   string `json:"client"`
	Scope    string `json:"scope"`
	Path     string `json:"path"`
	Server   string `json:"server"`
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

func findPlaintextSecrets(server clientMCPServer) []mcpSecretFinding {
	finding := func(location, reason string) mcpSecretFinding {
		return mcpSecretFinding{Client: server.Client, Scope: server.Scope, Path: server.Path, Server: server.Name, Location: location, Reason: reason}
	}
	findings := []mcpSecretFinding{}
	for _, key := range sortedMapKeys(server.Env) {
		if reason, ok := plaintextSecret(key, server.Env[key]); ok {
			findings = append(findings, finding("env."+key, reason))
		}
	}
	for _, key := range sortedMapKeys(server.Headers) {
		value := strings.TrimSpace(server.Headers[key])
		if (strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization")) && value != "" && !strings.Contains(value, "${") {
			findings = append(findings, finding("headers."+key, "literal credential header"))
			continue
		}
		if reason, ok := plaintextSecret(key, value); ok {
			findings = append(findings, finding("headers."+key, reason))
		}
	}
	for index := 0; index < len(server.Args); index++ {
		arg := server.Args[index]
		location := fmt.Sprintf("args[%d]", index)
		name, value, inline := strings.Cut(arg, "=")
		next := ""
		if index+1 < len(server.Args) {
			next = server.Args[index+1]
		}
		switch {
		case inline && strings.HasPrefix(name, "-") && secretFlagRE.MatchString(name) && value != "" && !envReference(value):
			findings = append(findings, finding(location, "secret flag with a literal value"))
		case secretFlagRE.MatchString(arg) && next != "" && !envReference(next):
			findings = append(findings, finding(fmt.Sprintf("args[%d]", index+1), "secret flag with a literal value"))
			index++
		case (arg == "--header" || arg == "-H") && literalAuthRE.MatchString(next) && !strings.Contains(next, "${"):
			findings = append(findings, finding(fmt.Sprintf("args[%d]", index+1), "literal credential header"))
			index++
		case arg == "-e" || arg == "--env":
			if key, literal, ok := strings.Cut(next, "="); ok {
				if reason, secret := plaintextSecret(key, literal); secret {
					findings = append(findings, finding(fmt.Sprintf("args[%d]", index+1), reason))
				}
			}
			index++
		case literalAuthRE.MatchString(arg) && !strings.Contains(arg, "${"):
			findings = append(findings, finding(location, "literal credential header"))
		case tokenValueRE.MatchString(arg):
			findings = append(findings, finding(location, "token-shaped value"))
		}
	}
	if server.URL != "" {
		if parsed, err := url.Parse(server.URL); err == nil {
			if parsed.User != nil {
				findings = append(findings, finding("url", "credential in the URL"))
			} else {
				for key, values := range parsed.Query() {
					if secretLikeName(key) || strings.EqualFold(key, "key") || strings.EqualFold(key, "sig") {
						if len(values) > 0 && values[0] != "" && !envReference(values[0]) {
							findings = append(findings, finding("url", "credential in the URL"))
							break
						}
					}
				}
			}
			if tokenValueRE.MatchString(parsed.Path) {
				findings = append(findings, finding("url", "token-shaped value in the URL path"))
			}
		}
	}
	return findings
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a *App) claudeStatePath() string {
	if dir := a.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(a.getenv("HOME"), ".claude.json")
}

func (a *App) codexHome() string {
	if home := a.getenv("CODEX_HOME"); home != "" {
		return home
	}
	return filepath.Join(a.getenv("HOME"), ".codex")
}

var claudeModeledKeys = map[string]bool{"type": true, "command": true, "args": true, "env": true, "url": true, "headers": true}

func claudeServers(path, scope string, raw map[string]map[string]any) []clientMCPServer {
	servers := make([]clientMCPServer, 0, len(raw))
	for name, entry := range raw {
		server := clientMCPServer{Client: "claude", Scope: scope, Path: path, Name: name,
			Type: anyString(entry["type"]), Command: anyString(entry["command"]), URL: anyString(entry["url"]),
			Args: anyStrings(entry["args"]), Env: anyStringMap(entry["env"]), Headers: anyStringMap(entry["headers"])}
		for key := range entry {
			if !claudeModeledKeys[key] {
				server.Unmodeled = append(server.Unmodeled, key)
			}
		}
		sort.Strings(server.Unmodeled)
		servers = append(servers, server)
	}
	return servers
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func anyStrings(value any) []string {
	items, _ := value.([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, fmt.Sprint(item))
	}
	return values
}

func anyStringMap(value any) map[string]string {
	items, _ := value.(map[string]any)
	values := make(map[string]string, len(items))
	for key, item := range items {
		values[key] = fmt.Sprint(item)
	}
	return values
}

// readClaudeMCPServers reads user and per-project servers from Claude's state
// file and project servers from .mcp.json in the working directory.
func (a *App) readClaudeMCPServers() ([]clientMCPServer, []unreadableConfig, error) {
	servers := []clientMCPServer{}
	unreadable := []unreadableConfig{}
	path := a.claudeStatePath()
	if b, err := os.ReadFile(path); err == nil {
		var state struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
			Projects   map[string]struct {
				MCPServers map[string]map[string]any `json:"mcpServers"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(b, &state); err != nil {
			unreadable = append(unreadable, unreadableConfig{Client: "claude", Path: path})
		} else {
			servers = append(servers, claudeServers(path, "user", state.MCPServers)...)
			for project, entry := range state.Projects {
				servers = append(servers, claudeServers(path, "local:"+project, entry.MCPServers)...)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	if cwd, err := os.Getwd(); err == nil {
		project := filepath.Join(cwd, ".mcp.json")
		if b, err := os.ReadFile(project); err == nil {
			var file struct {
				MCPServers map[string]map[string]any `json:"mcpServers"`
			}
			if json.Unmarshal(b, &file) != nil {
				unreadable = append(unreadable, unreadableConfig{Client: "claude", Path: project})
			} else {
				servers = append(servers, claudeServers(project, "project", file.MCPServers)...)
			}
		}
	}
	return servers, unreadable, nil
}

var codexModeledKeys = map[string]bool{"command": true, "args": true, "env": true, "url": true, "http_headers": true, "bearer_token": true}

// readCodexMCPServers reads [mcp_servers.*] from the user config and from a
// project .codex/config.toml in the working directory.
func (a *App) readCodexMCPServers() ([]clientMCPServer, []unreadableConfig, error) {
	paths := []struct{ path, scope string }{{filepath.Join(a.codexHome(), "config.toml"), "user"}}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, struct{ path, scope string }{filepath.Join(cwd, ".codex", "config.toml"), "project"})
	}
	servers := []clientMCPServer{}
	unreadable := []unreadableConfig{}
	for _, candidate := range paths {
		b, err := os.ReadFile(candidate.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", candidate.path, err)
		}
		var config struct {
			MCPServers map[string]map[string]any `toml:"mcp_servers"`
		}
		if _, err := toml.Decode(string(b), &config); err != nil {
			unreadable = append(unreadable, unreadableConfig{Client: "codex", Path: candidate.path})
			continue
		}
		for name, entry := range config.MCPServers {
			server := clientMCPServer{Client: "codex", Scope: candidate.scope, Path: candidate.path, Name: name,
				Command: anyString(entry["command"]), URL: anyString(entry["url"]), Args: anyStrings(entry["args"]),
				Env: anyStringMap(entry["env"]), Headers: anyStringMap(entry["http_headers"])}
			if token := anyString(entry["bearer_token"]); token != "" {
				server.Headers["Authorization"] = "Bearer " + token
			}
			for key := range entry {
				if !codexModeledKeys[key] {
					server.Unmodeled = append(server.Unmodeled, key)
				}
			}
			sort.Strings(server.Unmodeled)
			servers = append(servers, server)
		}
	}
	return servers, unreadable, nil
}

func (a *App) clientMCPServers() ([]clientMCPServer, []unreadableConfig, error) {
	claude, claudeUnreadable, err := a.readClaudeMCPServers()
	if err != nil {
		return nil, nil, err
	}
	codex, codexUnreadable, err := a.readCodexMCPServers()
	if err != nil {
		return nil, nil, err
	}
	all := append(claude, codex...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Client != all[j].Client {
			return all[i].Client < all[j].Client
		}
		if all[i].Scope != all[j].Scope {
			return all[i].Scope < all[j].Scope
		}
		return all[i].Name < all[j].Name
	})
	return all, append(claudeUnreadable, codexUnreadable...), nil
}

// mcpAuditSecrets reports plaintext credentials in client MCP configuration.
// It exits 1 when it finds any so it can gate a script.
func (a *App) mcpAuditSecrets(jsonMode bool) error {
	servers, unreadable, err := a.clientMCPServers()
	if err != nil {
		return err
	}
	findings := []mcpSecretFinding{}
	for _, config := range unreadable {
		findings = append(findings, mcpSecretFinding{Client: config.Client, Scope: "-", Path: config.Path, Server: "-", Location: "file", Reason: "could not parse; not inspected"})
	}
	for _, server := range servers {
		findings = append(findings, findPlaintextSecrets(server)...)
	}
	var outcome error
	if len(findings) > 0 {
		outcome = &CommandError{Code: "plaintext_secrets", Message: fmt.Sprintf("%d plaintext credential(s) in MCP client configuration", len(findings)), Exit: ExitOperational, Reported: jsonMode}
	}
	if jsonMode {
		if err := printEnvelope(a.out, map[string]any{"servers_inspected": len(servers), "findings": findings}, nil); err != nil {
			return err
		}
		return outcome
	}
	if len(findings) == 0 {
		fmt.Fprintf(a.err, "No plaintext credentials found in %d MCP server registration(s).\n", len(servers))
		return nil
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, f := range findings {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", f.Client, safeTerminalText(f.Scope), safeTerminalText(f.Server), safeTerminalText(f.Location), f.Reason)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(a.err, "Move a user-scope server into bb sec with: bb mcp adopt <claude|codex> <name> --service <service>; rotate any credential that sat in plaintext.")
	return outcome
}

// mcpAdopt moves a client-registered server's literal env and bearer token
// into bb sec, registers it in bb with references, and re-synchronizes the
// client so it starts the server through bb mcp launch / bb mcp header.
func (a *App) mcpAdopt(args []string) error {
	args, yes := takeFlag(args, "--yes")
	args, dropUnmodeled := takeFlag(args, "--drop-unmodeled")
	args, service, hasService, err := takeOptionValue(args, "--service")
	if err != nil {
		return err
	}
	if len(args) != 2 || (args[0] != "claude" && args[0] != "codex") || !hasService || !validSecretName(service) {
		return usage("mcp adopt", "<claude|codex> <name> --service <service> [--drop-unmodeled] [--yes]")
	}
	// Adopt chooses which service a client's token lands in, which can put it
	// within reach of "bb mcp serve"; it is an operator step.
	if !a.terminalAttached() && a.getenv(secretOutputOverrideEnv) != "1" {
		return invalid("bb mcp adopt moves credentials and requires a terminal")
	}
	client, name := args[0], args[1]
	if !validExplicitName(name) {
		return invalid("invalid MCP server name")
	}
	servers, _, err := a.clientMCPServers()
	if err != nil {
		return err
	}
	var found *clientMCPServer
	for index := range servers {
		if servers[index].Client == client && servers[index].Name == name {
			if servers[index].Scope != "user" {
				// bb synchronizes user-scope registrations only; moving a project
				// or local server would change who sees it.
				continue
			}
			found = &servers[index]
		}
	}
	if found == nil {
		return invalid(fmt.Sprintf("no user-scope %s MCP server named %s; project and local servers are not adopted", client, safeTerminalText(name)))
	}
	for _, f := range findPlaintextSecrets(*found) {
		if strings.HasPrefix(f.Location, "args") || f.Location == "url" {
			return invalid(fmt.Sprintf("%s has a credential in %s; move it to an environment variable first, then adopt", safeTerminalText(name), f.Location))
		}
	}
	if found.Type == "sse" {
		return invalid("SSE servers are not adopted; bb registers stdio and streamable HTTP only")
	}
	// Re-registration rebuilds the entry from what bb models; anything else
	// (Codex tool approvals, timeouts, cwd, env_vars) would be lost.
	if len(found.Unmodeled) > 0 && !dropUnmodeled {
		return invalid(fmt.Sprintf("re-registering %s would drop: %s; re-run with --drop-unmodeled to accept that", safeTerminalText(name), safeTerminalText(strings.Join(found.Unmodeled, ", "))))
	}
	server := mcpServer{Targets: []string{client}}
	fields := map[string]string{}
	if found.URL != "" {
		server.Transport, server.URL = "http", found.URL
		for key, value := range found.Headers {
			bearer, isBearer := strings.CutPrefix(strings.TrimSpace(value), "Bearer ")
			switch {
			case strings.EqualFold(key, "Authorization") && isBearer && !strings.Contains(bearer, "${"):
				if client == "codex" {
					return invalid("Codex has no headers helper, so bb cannot serve a bearer token to it; keep it in a wenv preset with --bearer-token-env")
				}
				fields["token"] = strings.TrimSpace(bearer)
				server.BearerTokenSecret = wenvSecretReferencePrefix + service + "/token"
			case strings.Contains(value, "${"):
				return invalid("header " + safeTerminalText(key) + " references the environment; bb cannot carry it over, so adopt by hand")
			default:
				return invalid("header " + safeTerminalText(key) + " is not a bearer token; bb registers only an Authorization bearer header for HTTP servers")
			}
		}
		if server.BearerTokenSecret == "" {
			return invalid(safeTerminalText(name) + " has no literal bearer token to adopt")
		}
	} else {
		server.Transport, server.Command, server.Args = "stdio", found.Command, found.Args
		server.SecretEnv = map[string]string{}
		for key, value := range found.Env {
			if strings.HasPrefix(strings.TrimSpace(value), wenvSecretReferencePrefix) || (!envReference(value) && strings.Contains(value, "${")) {
				return invalid("env " + safeTerminalText(key) + " holds a reference bb cannot carry over; adopt by hand")
			}
			if envReference(value) {
				server.EnvVars = append(server.EnvVars, strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "${"), "}"))
				continue
			}
			if !envKeyRE.MatchString(key) || !validSecretName(key) {
				return invalid("environment name " + safeTerminalText(key) + " cannot become a secret field")
			}
			// Every literal moves, not only secret-looking ones: the launcher
			// replaces the client's env block, so a plain URL left behind would
			// be lost.
			fields[key] = value
			server.SecretEnv[key] = wenvSecretReferencePrefix + service + "/" + key
		}
		if len(fields) == 0 {
			return invalid(safeTerminalText(name) + " has no literal environment values to adopt")
		}
	}
	server = normalizeMCPServer(server)
	if err := validateMCPServer(name, server); err != nil {
		return err
	}
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	if _, exists := store.Servers[name]; exists {
		return invalid("bb already has an MCP server named " + safeTerminalText(name) + "; remove it or adopt under the client's other name")
	}
	data, old, err := a.readSecretsSnapshot()
	if err != nil {
		return err
	}
	names := sortedMapKeys(fields)
	merged := map[string]string{}
	for field, value := range data[service] {
		merged[field] = value
	}
	for _, field := range names {
		if _, exists := data[service][field]; exists {
			return invalid(fmt.Sprintf("%s already exists in bb sec; choose another --service", secretKey(service, field)))
		}
		merged[field] = fields[field]
	}
	// "bb sec exec" derives one variable per field; names that collide after
	// upper-casing would break the whole service.
	if _, err := secretEnvironmentVariables(service, merged); err != nil {
		return invalid(fmt.Sprintf("adopting into %s would give two fields the same environment name; choose another --service", service))
	}
	keys := make([]string, len(names))
	for index, field := range names {
		keys[index] = secretKey(service, field)
	}
	fmt.Fprintf(a.out, "Adopt %s %s:\n  store in bb sec: %s\n  register in bb and re-sync %s through bb mcp %s\n",
		client, safeTerminalText(name), strings.Join(keys, ", "), client, map[bool]string{true: "header", false: "launch"}[server.Transport == "http"])
	if !yes {
		ok, err := a.confirmAction("Move these values out of " + found.Path + "?")
		if err != nil {
			return err
		}
		if !ok {
			return invalid("adopt cancelled")
		}
	}
	if data[service] == nil {
		data[service] = map[string]string{}
	}
	for field, value := range fields {
		data[service][field] = value
	}
	if err := a.writeSecrets(data, old); err != nil {
		return err
	}
	if err := a.updateMCP(func(store *mcpStore) error {
		if _, exists := store.Servers[name]; exists {
			return invalid("bb already has an MCP server named " + safeTerminalText(name))
		}
		store.Servers[name] = server
		return nil
	}); err != nil {
		// Take the fields back out so a retry starts clean.
		if current, snapshot, readErr := a.readSecretsSnapshot(); readErr == nil {
			for _, field := range names {
				delete(current[service], field)
			}
			if len(current[service]) == 0 {
				delete(current, service)
			}
			_ = a.writeSecrets(current, snapshot)
		}
		return fmt.Errorf("could not register the server (nothing adopted): %w", err)
	}
	if err := a.syncMCPServer(client, namedMCPServer{Name: name, mcpServer: server}); err != nil {
		return fmt.Errorf("stored the values and registered %s in bb, but re-registering it in %s failed and the old registration may already be removed; run: bb mcp sync %s %s: %w", name, client, client, name, err)
	}
	fmt.Fprintf(a.err, "Adopted %s: %s now starts it through bb. Client backups (such as %s.backup) may still hold the old value; rotate the credential.\n", name, client, found.Path)
	return nil
}
