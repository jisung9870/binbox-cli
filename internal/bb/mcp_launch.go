package bb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// normalizeSecretReference accepts "service/field" as shorthand for
// "sec://service/field"; validation happens in validateMCPServer.
func normalizeSecretReference(value string) string {
	if value == "" || strings.HasPrefix(value, wenvSecretReferencePrefix) {
		return value
	}
	return wenvSecretReferencePrefix + value
}

// mcpSecretReferences lists the sec:// references a server needs, sorted.
func mcpSecretReferences(server mcpServer) []string {
	refs := make([]string, 0, len(server.SecretEnv)+1)
	for _, reference := range server.SecretEnv {
		refs = append(refs, reference)
	}
	if server.BearerTokenSecret != "" {
		refs = append(refs, server.BearerTokenSecret)
	}
	return sortedUnique(refs)
}

// resolveMCPReferences decrypts the store once and returns each reference's
// value, failing on the first missing entry without naming any value.
func (a *App) resolveMCPReferences(refs []string) (map[string]string, error) {
	data, err := a.readSecrets()
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(refs))
	for _, reference := range refs {
		service, field, ok := parseWenvSecretReference(reference)
		if !ok {
			return nil, invalid("invalid secret reference: " + safeTerminalText(reference))
		}
		value, ok := data[service][field]
		if !ok {
			return nil, invalid("secret not found for MCP server: " + secretKey(service, field))
		}
		values[reference] = value
	}
	return values, nil
}

// expiryWarnings describes expired or soon-expiring entries among keys
// (service/field). Metadata problems produce no warning: they never block use.
func (a *App) expiryWarnings(keys []string) []string {
	meta, err := a.readSecretMeta()
	if err != nil {
		return nil
	}
	today := a.secretToday()
	warnings := []string{}
	for _, key := range sortedUnique(keys) {
		status := expiryStatus(key, meta[key], today, defaultExpiryWindowDays)
		if status.State == "expired" || status.State == "expiring" {
			warnings = append(warnings, fmt.Sprintf("%s (%s); renew it with: bb sec add %s --force", key, status.badge(), key))
		}
	}
	return warnings
}

func secretKeysForReferences(refs []string) []string {
	keys := make([]string, 0, len(refs))
	for _, reference := range refs {
		if service, field, ok := parseWenvSecretReference(reference); ok {
			keys = append(keys, secretKey(service, field))
		}
	}
	return keys
}

// warnExpiringMCPSecrets reports expired or soon-expiring references on stderr,
// which MCP clients keep in their server logs. It never blocks a launch.
func (a *App) warnExpiringMCPSecrets(name string, refs []string) {
	for _, warning := range a.expiryWarnings(secretKeysForReferences(refs)) {
		fmt.Fprintf(a.err, "bb: MCP server %s uses %s\n", name, warning)
	}
}

func (a *App) loadMCPServer(name string) (mcpServer, error) {
	store, _, err := a.loadMCP()
	if err != nil {
		return mcpServer{}, err
	}
	server, ok := store.Servers[name]
	if !ok {
		return mcpServer{}, invalid("MCP server not found: " + safeTerminalText(name))
	}
	if err := validateMCPServer(name, server); err != nil {
		return mcpServer{}, err
	}
	return server, nil
}

// mcpLaunch is the command MCP clients run for a stdio server with secret
// references. It resolves only that server's references and replaces itself
// with the server, so the values exist in one process environment.
func (a *App) mcpLaunch(args []string) error {
	if len(args) != 1 {
		return usage("mcp launch", "<name>")
	}
	name := args[0]
	server, err := a.loadMCPServer(name)
	if err != nil {
		return err
	}
	if server.Transport != "stdio" {
		return invalid("mcp launch runs stdio servers only")
	}
	refs := mcpSecretReferences(server)
	values, err := a.resolveMCPReferences(refs)
	if err != nil {
		return err
	}
	a.warnExpiringMCPSecrets(name, refs)
	a.markSecretsUsed(secretKeysForReferences(refs))
	names := make([]string, 0, len(server.SecretEnv))
	for variable := range server.SecretEnv {
		names = append(names, variable)
	}
	sort.Strings(names)
	variables := make([]secretEnvironmentVariable, len(names))
	for i, variable := range names {
		variables[i] = secretEnvironmentVariable{Name: variable, Value: values[server.SecretEnv[variable]]}
	}
	path, err := a.lookPath(server.Command)
	if err != nil {
		return unavailable("MCP server command not found: " + safeTerminalText(server.Command))
	}
	argv := append([]string{server.Command}, server.Args...)
	if err := a.execReplace(path, argv, overlaySecretEnvironment(a.env, variables)); err != nil {
		return fmt.Errorf("start MCP server %s: %w", name, err)
	}
	return nil
}

// mcpHeader is Claude's headersHelper for an HTTP server with a bearer secret.
// Its stdout is the token by design, so it refuses to run unless Claude names
// this exact server; that stops accidental use, not a deliberate same-user
// caller, which "bb sec" cannot distinguish from Claude.
func (a *App) mcpHeader(args []string) error {
	if len(args) != 1 {
		return usage("mcp header", "<name>")
	}
	name := args[0]
	if a.getenv("CLAUDE_CODE_MCP_SERVER_NAME") != name && a.getenv(secretOutputOverrideEnv) != "1" {
		return invalid("bb mcp header runs as Claude's headersHelper for " + safeTerminalText(name) + " only")
	}
	server, err := a.loadMCPServer(name)
	if err != nil {
		return err
	}
	if server.BearerTokenSecret == "" {
		return invalid("MCP server has no bearer token secret: " + safeTerminalText(name))
	}
	refs := mcpSecretReferences(server)
	values, err := a.resolveMCPReferences(refs)
	if err != nil {
		return err
	}
	a.warnExpiringMCPSecrets(name, refs)
	a.markSecretsUsed(secretKeysForReferences(refs))
	return json.NewEncoder(a.out).Encode(map[string]string{"Authorization": "Bearer " + values[server.BearerTokenSecret]})
}

// bbLauncherPath is the absolute bb that client configuration should call.
// Clients start servers without the interactive shell's PATH, so a bare "bb"
// may not resolve there.
func (a *App) bbLauncherPath() (string, error) {
	if path, err := a.lookPath("bb"); err == nil && filepath.IsAbs(path) {
		return path, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate bb for MCP launch: %w", err)
	}
	return path, nil
}
