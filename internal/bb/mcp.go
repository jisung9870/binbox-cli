package bb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	mcpStoreVersion = 1
	mcpAddAction    = "::add-server::"
)

type mcpStore struct {
	Version int                  `json:"version"`
	Servers map[string]mcpServer `json:"servers"`
}

type mcpServer struct {
	Description       string   `json:"description,omitempty"`
	Transport         string   `json:"transport"`
	Command           string   `json:"command,omitempty"`
	Args              []string `json:"args,omitempty"`
	URL               string   `json:"url,omitempty"`
	EnvVars           []string `json:"env_vars,omitempty"`
	BearerTokenEnvVar string   `json:"bearer_token_env_var,omitempty"`
	Targets           []string `json:"targets"`
}

type namedMCPServer struct {
	Name string `json:"name"`
	mcpServer
}

type mcpSpecOptions struct {
	server   mcpServer
	seen     map[string]bool
	position []string
}

func (a *App) mcpPath() (string, error) {
	config, _, err := a.paths()
	return filepath.Join(config, "mcp.json"), err
}

func (a *App) loadMCP() (mcpStore, string, error) {
	path, err := a.mcpPath()
	if err != nil {
		return mcpStore{}, path, err
	}
	store, err := readMCPStore(path)
	return store, path, err
}

func readMCPStore(path string) (mcpStore, error) {
	store := mcpStore{Version: mcpStoreVersion, Servers: map[string]mcpServer{}}
	err := readJSON(path, &store)
	if store.Version == 0 {
		store.Version = mcpStoreVersion
	}
	if store.Servers == nil {
		store.Servers = map[string]mcpServer{}
	}
	if err == nil && store.Version != mcpStoreVersion {
		err = invalid(fmt.Sprintf("unsupported MCP registry version %d", store.Version))
	}
	return store, err
}

func (a *App) updateMCP(update func(*mcpStore) error) error {
	path, err := a.mcpPath()
	if err != nil {
		return err
	}
	return withFileLock(path, func() error {
		store, err := readMCPStore(path)
		if err != nil {
			return err
		}
		if err := update(&store); err != nil {
			return err
		}
		return writeJSONAtomic(path, store)
	})
}

func (a *App) mcp(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb mcp                                   Open the MCP CRUD manager
  bb mcp list [--json]
  bb mcp show <name> [--json]
  bb mcp add <name> (--stdio COMMAND | --http URL) [options]
  bb mcp edit <name> [options]
  bb mcp rm <name> [--yes]
  bb mcp sync <claude|codex> [name]
  bb mcp check [name] [--json]
  bb mcp audit [--json]

Options for add/edit:
  --description TEXT          Human-readable purpose
  --arg ARG                   Stdio argument (repeatable)
  --env VAR                   Required inherited environment variable (repeatable)
  --bearer-token-env VAR      HTTP bearer-token environment variable
  --targets claude,codex      Registration targets (default: both)
  --clear-args                Remove all stdio arguments when editing
  --clear-env                 Remove all required environment variables
  --clear-bearer-token-env    Remove HTTP bearer-token environment variable

Secret values are never stored here. Put them in bb sec, reference them from a
wenv preset, then start Claude/Codex from that applied environment.
`)
		return err
	}
	if len(args) == 0 {
		return a.mcpManage()
	}
	switch args[0] {
	case "list", "ls":
		return a.mcpList(args[1:])
	case "show":
		return a.mcpShow(args[1:])
	case "add":
		return a.mcpAdd(args[1:])
	case "edit":
		return a.mcpEdit(args[1:])
	case "rm", "remove":
		return a.mcpRemove(args[1:])
	case "sync":
		return a.mcpSync(args[1:])
	case "check":
		return a.mcpCheck(args[1:])
	case "audit":
		return a.mcpAudit(args[1:])
	default:
		return invalid("unknown mcp command")
	}
}

func parseMCPOptions(args []string) (mcpSpecOptions, error) {
	opts := mcpSpecOptions{seen: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if !strings.HasPrefix(flag, "--") {
			opts.position = append(opts.position, flag)
			continue
		}
		switch flag {
		case "--clear-args":
			opts.server.Args, opts.seen["args"] = []string{}, true
			continue
		case "--clear-env":
			opts.server.EnvVars, opts.seen["env"] = []string{}, true
			continue
		case "--clear-bearer-token-env":
			opts.server.BearerTokenEnvVar, opts.seen["bearer"] = "", true
			continue
		}
		if i+1 >= len(args) {
			return opts, invalid(flag + " requires a value")
		}
		value := args[i+1]
		i++
		switch flag {
		case "--description":
			opts.server.Description, opts.seen["description"] = value, true
		case "--stdio":
			opts.server.Transport, opts.server.Command = "stdio", value
			opts.seen["transport"] = true
		case "--http":
			opts.server.Transport, opts.server.URL = "http", value
			opts.seen["transport"] = true
		case "--arg":
			opts.server.Args = append(opts.server.Args, value)
			opts.seen["args"] = true
		case "--env":
			opts.server.EnvVars = append(opts.server.EnvVars, value)
			opts.seen["env"] = true
		case "--bearer-token-env":
			opts.server.BearerTokenEnvVar, opts.seen["bearer"] = value, true
		case "--targets":
			opts.server.Targets = splitMCPList(value)
			opts.seen["targets"] = true
		default:
			return opts, invalid("unknown MCP option: " + flag)
		}
	}
	return opts, nil
}

func splitMCPList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeMCPServer(server mcpServer) mcpServer {
	server.EnvVars = sortedUnique(server.EnvVars)
	server.Targets = sortedUnique(server.Targets)
	if server.Args == nil {
		server.Args = []string{}
	}
	if server.EnvVars == nil {
		server.EnvVars = []string{}
	}
	return server
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func validateMCPServer(name string, server mcpServer) error {
	if !presetNameRE.MatchString(name) {
		return invalid("MCP names may contain only letters, digits, dot, underscore, and hyphen")
	}
	if len(server.Targets) == 0 {
		return invalid("MCP server requires at least one target")
	}
	for _, target := range server.Targets {
		if target != "claude" && target != "codex" {
			return invalid("MCP targets must be claude or codex")
		}
	}
	for _, variable := range server.EnvVars {
		if !envKeyRE.MatchString(variable) {
			return invalid("MCP environment entries must be variable names, not values")
		}
	}
	if server.BearerTokenEnvVar != "" && !envKeyRE.MatchString(server.BearerTokenEnvVar) {
		return invalid("MCP bearer token must name an environment variable")
	}
	switch server.Transport {
	case "stdio":
		if !validExplicitName(server.Command) {
			return invalid("stdio MCP server requires a command")
		}
		if server.URL != "" || server.BearerTokenEnvVar != "" {
			return invalid("stdio MCP server cannot use HTTP options")
		}
	case "http":
		parsed, err := url.Parse(server.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return invalid("HTTP MCP server requires an http(s) URL without embedded credentials")
		}
		if server.Command != "" || len(server.Args) != 0 {
			return invalid("HTTP MCP server cannot use stdio command options")
		}
	default:
		return invalid("MCP transport must be stdio or http")
	}
	return nil
}

func (a *App) mcpAdd(args []string) error {
	opts, err := parseMCPOptions(args)
	if err != nil || len(opts.position) != 1 {
		if err != nil {
			return err
		}
		return usage("mcp add", "<name> (--stdio COMMAND | --http URL) [options]")
	}
	if !opts.seen["targets"] {
		opts.server.Targets = []string{"claude", "codex"}
	}
	server := normalizeMCPServer(opts.server)
	if err := validateMCPServer(opts.position[0], server); err != nil {
		return err
	}
	name := opts.position[0]
	return a.updateMCP(func(store *mcpStore) error {
		if _, exists := store.Servers[name]; exists {
			return invalid("MCP server already exists: " + name)
		}
		store.Servers[name] = server
		return nil
	})
}

func (a *App) mcpEdit(args []string) error {
	opts, err := parseMCPOptions(args)
	if err != nil || len(opts.position) != 1 {
		if err != nil {
			return err
		}
		return usage("mcp edit", "<name> [options]")
	}
	name := opts.position[0]
	return a.updateMCP(func(store *mcpStore) error {
		server, exists := store.Servers[name]
		if !exists {
			return invalid("MCP server not found: " + name)
		}
		if opts.seen["description"] {
			server.Description = opts.server.Description
		}
		if opts.seen["targets"] {
			server.Targets = opts.server.Targets
		}
		if opts.seen["env"] {
			server.EnvVars = opts.server.EnvVars
		}
		if opts.seen["bearer"] {
			server.BearerTokenEnvVar = opts.server.BearerTokenEnvVar
		}
		if opts.seen["args"] {
			server.Args = opts.server.Args
		}
		if opts.seen["transport"] {
			server.Transport, server.Command, server.URL = opts.server.Transport, opts.server.Command, opts.server.URL
			if server.Transport == "stdio" {
				server.BearerTokenEnvVar = ""
			} else {
				server.Args = nil
			}
		}
		server = normalizeMCPServer(server)
		if err := validateMCPServer(name, server); err != nil {
			return err
		}
		store.Servers[name] = server
		return nil
	})
}

func namedMCPServers(store mcpStore) []namedMCPServer {
	items := make([]namedMCPServer, 0, len(store.Servers))
	for name, server := range store.Servers {
		items = append(items, namedMCPServer{Name: name, mcpServer: server})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (a *App) mcpList(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 0 {
		return usage("mcp list", "[--json]")
	}
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	items := namedMCPServers(store)
	if jsonMode {
		return printEnvelope(a.out, items, nil)
	}
	return printHuman(a.out, items)
}

func (a *App) mcpShow(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 1 {
		return usage("mcp show", "<name> [--json]")
	}
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	server, exists := store.Servers[args[0]]
	if !exists {
		return invalid("MCP server not found: " + args[0])
	}
	item := namedMCPServer{Name: args[0], mcpServer: server}
	if jsonMode {
		return printEnvelope(a.out, item, nil)
	}
	return printHuman(a.out, item)
}

func (a *App) mcpRemove(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 1 {
		return usage("mcp rm", "<name> [--yes]")
	}
	name := args[0]
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	if _, exists := store.Servers[name]; !exists {
		return invalid("MCP server not found: " + name)
	}
	if !yes {
		confirmed, err := a.confirmAction("Remove MCP server " + args[0] + " from the bb registry?")
		if err != nil {
			return err
		}
		if !confirmed {
			return invalid("MCP removal cancelled")
		}
	}
	return a.updateMCP(func(store *mcpStore) error {
		if _, exists := store.Servers[name]; !exists {
			return invalid("MCP server not found: " + name)
		}
		delete(store.Servers, name)
		return nil
	})
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (a *App) mcpSync(args []string) error {
	if len(args) < 1 || len(args) > 2 || (args[0] != "claude" && args[0] != "codex") {
		return usage("mcp sync", "<claude|codex> [name]")
	}
	target := args[0]
	if _, err := a.lookPath(target); err != nil {
		return unavailable(target + " is required for MCP sync")
	}
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	items := namedMCPServers(store)
	if len(args) == 2 {
		server, exists := store.Servers[args[1]]
		if !exists {
			return invalid("MCP server not found: " + args[1])
		}
		items = []namedMCPServer{{Name: args[1], mcpServer: server}}
	}
	synced := 0
	for _, item := range items {
		if !containsString(item.Targets, target) {
			continue
		}
		if err := validateMCPServer(item.Name, item.mcpServer); err != nil {
			return err
		}
		if err := a.syncMCPServer(target, item); err != nil {
			return err
		}
		synced++
	}
	if len(args) == 2 && synced == 0 {
		return invalid("MCP server does not target " + target)
	}
	_, err = fmt.Fprintf(a.out, "Synchronized %d MCP server(s) to %s.\n", synced, target)
	return err
}

func (a *App) syncMCPServer(target string, item namedMCPServer) error {
	removeArgs := []string{"mcp", "remove"}
	if target == "claude" {
		removeArgs = append(removeArgs, "--scope", "user")
	}
	removeArgs = append(removeArgs, item.Name)
	cmd := a.command(target, removeArgs...)
	cmd.Env = a.env
	_ = cmd.Run()

	argv := []string{"mcp", "add"}
	if target == "claude" {
		argv = append(argv, "--scope", "user")
	}
	if item.Transport == "http" {
		if target == "codex" {
			argv = append(argv, item.Name, "--url", item.URL)
			if item.BearerTokenEnvVar != "" {
				argv = append(argv, "--bearer-token-env-var", item.BearerTokenEnvVar)
			}
		} else {
			argv = append(argv, "--transport", "http", item.Name, item.URL)
			if item.BearerTokenEnvVar != "" {
				argv = append(argv, "--header", "Authorization: Bearer ${"+item.BearerTokenEnvVar+"}")
			}
		}
	} else {
		argv = append(argv, item.Name, "--", item.Command)
		argv = append(argv, item.Args...)
	}
	cmd = a.command(target, argv...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = a.env, a.in, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("synchronize MCP server %s to %s: %w", item.Name, target, err)
	}
	return nil
}

type mcpCheckResult struct {
	Name       string            `json:"name"`
	Valid      bool              `json:"valid"`
	Executable bool              `json:"executable,omitempty"`
	MissingEnv []string          `json:"missing_env"`
	Targets    map[string]string `json:"targets"`
}

func (a *App) mcpCheck(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) > 1 {
		return usage("mcp check", "[name] [--json]")
	}
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	items := namedMCPServers(store)
	if len(args) == 1 {
		server, exists := store.Servers[args[0]]
		if !exists {
			return invalid("MCP server not found: " + args[0])
		}
		items = []namedMCPServer{{Name: args[0], mcpServer: server}}
	}
	results := make([]mcpCheckResult, 0, len(items))
	for _, item := range items {
		result := mcpCheckResult{Name: item.Name, Valid: validateMCPServer(item.Name, item.mcpServer) == nil, MissingEnv: []string{}, Targets: map[string]string{}}
		if item.Transport == "stdio" {
			_, lookupErr := a.lookPath(item.Command)
			result.Executable = lookupErr == nil
		}
		variables := append([]string{}, item.EnvVars...)
		if item.BearerTokenEnvVar != "" {
			variables = append(variables, item.BearerTokenEnvVar)
		}
		for _, variable := range sortedUnique(variables) {
			if a.getenv(variable) == "" {
				result.MissingEnv = append(result.MissingEnv, variable)
			}
		}
		for _, target := range item.Targets {
			if _, lookupErr := a.lookPath(target); lookupErr != nil {
				result.Targets[target] = "client missing"
				continue
			}
			argv := []string{"mcp", "get", item.Name}
			if target == "codex" {
				argv = append(argv, "--json")
			}
			output, getErr := a.readCommand(target, argv...)
			if getErr != nil {
				result.Targets[target] = "not registered or unhealthy"
			} else if target == "claude" {
				result.Targets[target] = claudeMCPHealth(output)
			} else {
				result.Targets[target] = "registered"
			}
		}
		results = append(results, result)
	}
	if jsonMode {
		return printEnvelope(a.out, results, nil)
	}
	return printHuman(a.out, results)
}

func claudeMCPHealth(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "failed to connect"), strings.Contains(lower, "disconnected"):
		return "registered but unhealthy"
	case strings.Contains(lower, "pending approval"):
		return "pending approval"
	case strings.Contains(lower, "connected"):
		return "registered and healthy"
	default:
		return "registered; health unknown"
	}
}

func (a *App) mcpAudit(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 0 {
		return usage("mcp audit", "[--json]")
	}
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	candidates := []string{filepath.Join(config, "mcp.json"), filepath.Join(a.getenv("HOME"), ".claude.json"), filepath.Join(a.getenv("HOME"), ".codex", "config.toml")}
	type item struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
		SHA256 string `json:"sha256,omitempty"`
	}
	items := make([]item, 0, len(candidates))
	for _, path := range candidates {
		b, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			items = append(items, item{Path: path})
			continue
		}
		if readErr != nil {
			return fmt.Errorf("read MCP config %s: %w", path, readErr)
		}
		sum := sha256.Sum256(b)
		items = append(items, item{Path: path, Exists: true, SHA256: hex.EncodeToString(sum[:])})
	}
	data := map[string]any{"mode": "audit", "read_only": true, "content_inspected": false, "items": items}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

func mcpChoices(store mcpStore) []selectChoice {
	choices := make([]selectChoice, 0, len(store.Servers)+1)
	for _, item := range namedMCPServers(store) {
		choices = append(choices, selectChoice{Value: item.Name, Label: item.Name, Description: item.Transport + " · " + strings.Join(item.Targets, ","), SearchText: item.Description})
	}
	return append(choices, selectChoice{Value: mcpAddAction, Label: "Add server", Description: "Create an MCP registration"})
}

func (a *App) mcpManage() error {
	store, _, err := a.loadMCP()
	if err != nil {
		return err
	}
	root := selectStage{Prompt: "MCP server", Choices: mcpChoices(store)}
	next := func(path []string) *selectStage {
		if len(path) != 1 || path[0] == mcpAddAction {
			return nil
		}
		return &selectStage{Prompt: "Action for " + path[0], Choices: []selectChoice{
			{Value: "show", Label: "Show", Description: "Inspect registration"},
			{Value: "edit", Label: "Edit", Description: "Replace registration fields"},
			{Value: "sync-claude", Label: "Sync Claude", Description: "Register through Claude CLI"},
			{Value: "sync-codex", Label: "Sync Codex", Description: "Register through Codex CLI"},
			{Value: "check", Label: "Check", Description: "Validate environment and clients"},
			{Value: "remove", Label: "Remove", Description: "Delete from bb registry only"},
		}}
	}
	outcome, err := a.selectStages(root, next)
	if err != nil || outcome.Cancelled || len(outcome.Path) == 0 {
		return err
	}
	if outcome.Path[0] == mcpAddAction {
		return a.promptMCPAdd(store)
	}
	if len(outcome.Path) < 2 {
		return nil
	}
	name, action := outcome.Path[0], outcome.Path[1]
	switch action {
	case "show":
		return a.mcpShow([]string{name})
	case "edit":
		return a.promptMCPEdit(name, store.Servers[name])
	case "sync-claude":
		return a.mcpSync([]string{"claude", name})
	case "sync-codex":
		return a.mcpSync([]string{"codex", name})
	case "check":
		return a.mcpCheck([]string{name})
	case "remove":
		return a.mcpRemove([]string{name})
	default:
		return invalid("unknown MCP action")
	}
}

func (a *App) promptMCPLine(label, current string) (string, bool, error) {
	if current == "" {
		fmt.Fprintf(a.err, "%s [Enter=cancel]: ", label)
	} else {
		fmt.Fprintf(a.err, "%s [%s]: ", label, safeTerminalText(current))
	}
	value, err := readLine(a.in)
	value = strings.TrimSpace(value)
	if err != nil && value == "" {
		return "", false, err
	}
	if value == "" && current != "" {
		return current, true, nil
	}
	return value, value != "", nil
}

func (a *App) promptMCPOptionalLine(label, current string) (string, error) {
	if current == "" {
		fmt.Fprintf(a.err, "%s [Enter=empty]: ", label)
	} else {
		fmt.Fprintf(a.err, "%s [%s, -=clear]: ", label, safeTerminalText(current))
	}
	value, err := readLine(a.in)
	value = strings.TrimSpace(value)
	if err != nil && value == "" {
		return "", err
	}
	if value == "" {
		return current, nil
	}
	if value == "-" {
		return "", nil
	}
	return value, nil
}

func (a *App) promptMCPAdd(store mcpStore) error {
	name, ok, err := a.promptMCPLine("Name", "")
	if err != nil || !ok {
		return err
	}
	if _, exists := store.Servers[name]; exists {
		return invalid("MCP server already exists: " + name)
	}
	return a.promptMCPSpec(name, mcpServer{Targets: []string{"claude", "codex"}}, true)
}

func (a *App) promptMCPEdit(name string, server mcpServer) error {
	return a.promptMCPSpec(name, server, false)
}

func (a *App) promptMCPSpec(name string, current mcpServer, add bool) error {
	description, err := a.promptMCPOptionalLine("Description", current.Description)
	if err != nil {
		return err
	}
	transport, ok, err := a.promptMCPLine("Transport (stdio/http)", current.Transport)
	if err != nil || !ok {
		return err
	}
	server := current
	server.Description, server.Transport = description, transport
	if transport == "stdio" {
		command, ok, err := a.promptMCPLine("Command", current.Command)
		if err != nil || !ok {
			return err
		}
		args, err := a.promptMCPOptionalLine("Arguments (comma-separated)", strings.Join(current.Args, ","))
		if err != nil {
			return err
		}
		server.Command, server.Args, server.URL, server.BearerTokenEnvVar = command, splitMCPList(args), "", ""
	} else {
		address, ok, err := a.promptMCPLine("URL", current.URL)
		if err != nil || !ok {
			return err
		}
		bearer, err := a.promptMCPOptionalLine("Bearer token env var (optional)", current.BearerTokenEnvVar)
		if err != nil {
			return err
		}
		server.URL, server.BearerTokenEnvVar, server.Command, server.Args = address, bearer, "", nil
	}
	envVars, err := a.promptMCPOptionalLine("Required env vars (comma-separated)", strings.Join(current.EnvVars, ","))
	if err != nil {
		return err
	}
	targets, ok, err := a.promptMCPLine("Targets (claude,codex)", strings.Join(current.Targets, ","))
	if err != nil || !ok {
		return err
	}
	server.EnvVars, server.Targets = splitMCPList(envVars), splitMCPList(targets)
	server = normalizeMCPServer(server)
	if err := validateMCPServer(name, server); err != nil {
		return err
	}
	return a.updateMCP(func(store *mcpStore) error {
		if add {
			if _, exists := store.Servers[name]; exists {
				return invalid("MCP server already exists: " + name)
			}
		} else if _, exists := store.Servers[name]; !exists {
			return invalid("MCP server not found: " + name)
		}
		store.Servers[name] = server
		return nil
	})
}
