package bb

// bb mcp serve exposes the secret and environment stores to MCP clients without
// ever placing secret material in a tool result. Agents receive sec:// handles
// and delegate execution; values travel from the encrypted store into a child
// process environment and nowhere else. A tool result is model context: once a
// value lands there it also lands in transcripts and summaries, so the surface
// here has no value-returning tool at all.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	mcpServeConfigVersion   = 1
	mcpServeLatestProtocol  = "2025-06-18"
	mcpServeMaxRequestBytes = 1 << 20
	mcpServeMaxOutputBytes  = 64 << 10
	mcpServeExecTimeout     = 2 * time.Minute
	// mcpServeMinRedactBytes keeps redaction from shredding ordinary output when
	// a stored value is a single character. Values below it are reported instead.
	mcpServeMinRedactBytes = 4
)

// mcpServeProtocols is ordered newest first. bb answers initialize with the
// client's version when it is one of these, and with its newest otherwise.
var mcpServeProtocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpServeInstructions = `bb exposes the local encrypted secret store (bb sec) and declarative
environment presets (bb wenv).

Secret values are never returned by any tool. Work with sec://<service>/<field>
references, store them in wenv presets, and run commands that need the value
through sec_exec or wenv_exec, which inject it into a child process environment
and return only that process output. Executable commands are restricted to an
operator-managed allowlist (bb mcp allow).`

// mcpExecRule permits one command through sec_exec/wenv_exec. An empty Args
// permits any arguments; otherwise argv[1:] must start with Args, which is how a
// read-only subcommand is allowed without allowing its destructive siblings.
type mcpExecRule struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

func (r mcpExecRule) String() string {
	return strings.Join(append([]string{r.Command}, r.Args...), " ")
}

func (r mcpExecRule) permits(argv []string) bool {
	if len(argv) == 0 || r.Command != argv[0] || len(r.Args) > len(argv)-1 {
		return false
	}
	for index, want := range r.Args {
		if argv[index+1] != want {
			return false
		}
	}
	return true
}

// mcpServeConfig holds the exec allowlist. Commands are fail-closed: with no
// rule, sec_exec and wenv_exec run nothing. The service and preset scopes are
// opt-in narrowing on top of that; empty means every service or preset the
// command allowlist already permits.
type mcpServeConfig struct {
	Version      int           `json:"version"`
	ExecRules    []mcpExecRule `json:"exec_rules"`
	ExecServices []string      `json:"exec_services"`
	ExecPresets  []string      `json:"exec_presets"`
}

func (c mcpServeConfig) rulesFor(command string) []mcpExecRule {
	matches := make([]mcpExecRule, 0, len(c.ExecRules))
	for _, rule := range c.ExecRules {
		if rule.Command == command {
			matches = append(matches, rule)
		}
	}
	return matches
}

func (c mcpServeConfig) permits(argv []string) bool {
	for _, rule := range c.ExecRules {
		if rule.permits(argv) {
			return true
		}
	}
	return false
}

func newMCPServeConfig() mcpServeConfig {
	return mcpServeConfig{Version: mcpServeConfigVersion, ExecRules: []mcpExecRule{}, ExecServices: []string{}, ExecPresets: []string{}}
}

func normalizeMCPServeConfig(config *mcpServeConfig) {
	if config.ExecRules == nil {
		config.ExecRules = []mcpExecRule{}
	}
	seen := map[string]bool{}
	rules := make([]mcpExecRule, 0, len(config.ExecRules))
	for _, rule := range config.ExecRules {
		if rule.Args == nil {
			rule.Args = []string{}
		}
		key := rule.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].String() < rules[j].String() })
	config.ExecRules = rules
	config.ExecServices = sortedUnique(config.ExecServices)
	config.ExecPresets = sortedUnique(config.ExecPresets)
}

func (a *App) mcpServeConfigPath() (string, error) {
	config, _, err := a.paths()
	return filepath.Join(config, "mcp-serve.json"), err
}

func readMCPServeConfig(path string) (mcpServeConfig, error) {
	config := newMCPServeConfig()
	if err := readJSON(path, &config); err != nil {
		return config, fmt.Errorf("read MCP serve config: %w", err)
	}
	if config.Version == 0 {
		config.Version = mcpServeConfigVersion
	}
	if config.Version != mcpServeConfigVersion {
		return config, invalid(fmt.Sprintf("unsupported MCP serve config version %d", config.Version))
	}
	normalizeMCPServeConfig(&config)
	return config, nil
}

func (a *App) loadMCPServeConfig() (mcpServeConfig, error) {
	path, err := a.mcpServeConfigPath()
	if err != nil {
		return newMCPServeConfig(), err
	}
	return readMCPServeConfig(path)
}

func (a *App) updateMCPServeConfig(update func(*mcpServeConfig) error) error {
	path, err := a.mcpServeConfigPath()
	if err != nil {
		return err
	}
	return withFileLock(path, func() error {
		config, err := readMCPServeConfig(path)
		if err != nil {
			return err
		}
		if err := update(&config); err != nil {
			return err
		}
		normalizeMCPServeConfig(&config)
		return writeJSONAtomic(path, config)
	})
}

// splitMCPAllowArgs reads "<command>..." or "<command> -- <argument prefix>...".
// Everything after "--" is taken literally, so an argument prefix may contain
// flags without bb trying to interpret them.
func splitMCPAllowArgs(args []string) (names []string, prefix []string, restricted bool, err error) {
	for index, arg := range args {
		if arg != "--" {
			continue
		}
		if index != 1 {
			return nil, nil, false, invalid("an argument prefix applies to exactly one command: bb mcp allow <action> <command> -- <argument>...")
		}
		if index+1 >= len(args) {
			return nil, nil, false, invalid("-- requires at least one argument")
		}
		return args[:1], args[index+1:], true, nil
	}
	return args, nil, false, nil
}

func validMCPExecCommand(name string) bool {
	return validExplicitName(name) && !strings.ContainsAny(name, "/ \t")
}

func (a *App) mcpAllow(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return usage("mcp allow list", "[--json]")
		}
		return a.mcpAllowList(jsonMode)
	}
	if args[0] == "service" || args[0] == "preset" {
		return a.mcpAllowScope(args[0], args[1:])
	}
	action := args[0]
	if action != "add" && action != "rm" && action != "remove" {
		return usage("mcp allow", "[list] | add|rm <command> [-- <argument>...] | service|preset add|rm <name>...")
	}
	names, prefix, restricted, err := splitMCPAllowArgs(args[1:])
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return usage("mcp allow "+action, "<command> [-- <argument>...]")
	}
	for _, name := range names {
		if !validMCPExecCommand(name) {
			return invalid("MCP exec allowlist entries must be bare command names resolved through PATH")
		}
	}
	for _, argument := range prefix {
		if argument == "" || strings.ContainsRune(argument, 0) {
			return invalid("argument prefix entries must be non-empty and free of NUL")
		}
	}
	if action == "add" {
		return a.mcpAllowAdd(names, prefix, restricted)
	}
	return a.mcpAllowRemove(names, prefix, restricted)
}

func (a *App) mcpAllowList(jsonMode bool) error {
	config, err := a.loadMCPServeConfig()
	if err != nil {
		return err
	}
	commands := make([]string, 0, len(config.ExecRules))
	for _, rule := range config.ExecRules {
		commands = append(commands, rule.String())
	}
	data := map[string]any{
		"exec_commands":   commands,
		"exec_enabled":    len(config.ExecRules) > 0,
		"exec_services":   config.ExecServices,
		"exec_presets":    config.ExecPresets,
		"services_scoped": len(config.ExecServices) > 0,
		"presets_scoped":  len(config.ExecPresets) > 0,
	}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

func (a *App) mcpAllowAdd(names, prefix []string, restricted bool) error {
	return a.updateMCPServeConfig(func(config *mcpServeConfig) error {
		for _, name := range names {
			existing := config.rulesFor(name)
			// Refuse to let a broad rule and a narrow one coexist: an operator who
			// adds "aws sts get-caller-identity" on top of a bare "aws" has narrowed
			// nothing, and nothing in the listing would say so.
			for _, rule := range existing {
				if len(rule.Args) == 0 && restricted {
					return invalid(name + " is already allowed with any arguments; remove that entry before narrowing it")
				}
				if len(rule.Args) > 0 && !restricted {
					return invalid(name + " already has argument-restricted entries; remove them before allowing any arguments")
				}
			}
			config.ExecRules = append(config.ExecRules, mcpExecRule{Command: name, Args: prefix})
		}
		return nil
	})
}

func (a *App) mcpAllowRemove(names, prefix []string, restricted bool) error {
	target := mcpExecRule{Command: names[0], Args: prefix}
	return a.updateMCPServeConfig(func(config *mcpServeConfig) error {
		kept := make([]mcpExecRule, 0, len(config.ExecRules))
		removed := 0
		for _, rule := range config.ExecRules {
			// Without "--" the removal covers every rule for the command, so an
			// operator can revoke a command without restating each prefix.
			drop := restricted && rule.String() == target.String()
			if !restricted && containsString(names, rule.Command) {
				drop = true
			}
			if drop {
				removed++
				continue
			}
			kept = append(kept, rule)
		}
		if removed == 0 {
			return invalid("no matching MCP exec allowlist entry")
		}
		config.ExecRules = kept
		return nil
	})
}

func (a *App) mcpAllowScope(kind string, args []string) error {
	if len(args) < 2 || (args[0] != "add" && args[0] != "rm" && args[0] != "remove") {
		return usage("mcp allow "+kind, "add|rm <name>...")
	}
	action, names := args[0], args[1:]
	for _, name := range names {
		if !presetNameRE.MatchString(name) {
			return invalid("MCP " + kind + " scope entries may contain only letters, digits, dot, underscore, and hyphen")
		}
	}
	return a.updateMCPServeConfig(func(config *mcpServeConfig) error {
		scope := &config.ExecServices
		if kind == "preset" {
			scope = &config.ExecPresets
		}
		if action == "add" {
			*scope = append(*scope, names...)
			return nil
		}
		kept := make([]string, 0, len(*scope))
		removed := 0
		for _, existing := range *scope {
			if containsString(names, existing) {
				removed++
				continue
			}
			kept = append(kept, existing)
		}
		if removed == 0 {
			return invalid("no matching MCP " + kind + " scope entry")
		}
		*scope = kept
		return nil
	})
}

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func jsonrpcResult(id json.RawMessage, result any) jsonrpcResponse {
	return jsonrpcResponse{JSONRPC: "2.0", ID: normalizeJSONRPCID(id), Result: result}
}

func jsonrpcFailure(id json.RawMessage, code int, message string) jsonrpcResponse {
	return jsonrpcResponse{JSONRPC: "2.0", ID: normalizeJSONRPCID(id), Error: &jsonrpcError{Code: code, Message: message}}
}

func normalizeJSONRPCID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// isJSONRPCNotification reports whether a message carries no id and therefore
// must not be answered.
func isJSONRPCNotification(id json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(id))
	return trimmed == "" || trimmed == "null"
}

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	handler     func(*App, json.RawMessage) (any, error)
}

func mcpObjectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func mcpStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func mcpArgvSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"minItems":    1,
		"description": "Command and arguments. argv[0] must be on the bb mcp allow list. No shell is involved.",
	}
}

func mcpServeTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "sec_list",
			Title:       "List secret services and fields",
			Description: "List the service and field names in the encrypted store. Values are never included.",
			InputSchema: mcpObjectSchema(map[string]any{
				"service": mcpStringSchema("Restrict the listing to one service."),
			}),
			handler: (*App).mcpToolSecList,
		},
		{
			Name:        "sec_ref",
			Title:       "Build a secret reference",
			Description: "Validate a secret entry and return its sec://<service>/<field> reference. Use the reference wherever a value would otherwise be pasted; the value itself is never returned.",
			InputSchema: mcpObjectSchema(map[string]any{
				"service": mcpStringSchema("Secret service name."),
				"field":   mcpStringSchema("Secret field name. Optional when the service holds exactly one field."),
			}, "service"),
			handler: (*App).mcpToolSecRef,
		},
		{
			Name:        "wenv_list",
			Title:       "List environment presets",
			Description: "List declarative environment preset names and their variable counts.",
			InputSchema: mcpObjectSchema(map[string]any{}),
			handler:     (*App).mcpToolWenvList,
		},
		{
			Name:        "wenv_show",
			Title:       "Show an environment preset",
			Description: "Show a preset's variables. Secret references stay as sec://<service>/<field> and are never resolved here.",
			InputSchema: mcpObjectSchema(map[string]any{
				"preset": mcpStringSchema("Preset name."),
			}, "preset"),
			handler: (*App).mcpToolWenvShow,
		},
		{
			Name:        "wenv_set",
			Title:       "Set environment preset variables",
			Description: "Create or update variables in a preset, keeping variables that are not listed. Secret-like variables must be assigned a sec://<service>/<field> reference; plaintext secrets are rejected.",
			InputSchema: mcpObjectSchema(map[string]any{
				"preset": mcpStringSchema("Preset name."),
				"assignments": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"minItems":    1,
					"description": "KEY=VALUE assignments, e.g. CONTROLLER_OAUTH_TOKEN=sec://awx/w-token.",
				},
			}, "preset", "assignments"),
			handler: (*App).mcpToolWenvSet,
		},
		{
			Name:        "sec_exec",
			Title:       "Run a command with a secret service",
			Description: "Run an allowlisted command with one secret service exported into its environment, and return only that command's output. The values themselves are redacted from the output.",
			InputSchema: mcpObjectSchema(map[string]any{
				"service": mcpStringSchema("Secret service whose fields become environment variables."),
				"argv":    mcpArgvSchema(),
			}, "service", "argv"),
			handler: (*App).mcpToolSecExec,
		},
		{
			Name:        "wenv_exec",
			Title:       "Run a command with an environment preset",
			Description: "Run an allowlisted command with a resolved environment preset, and return only that command's output. Values behind sec:// references are redacted from the output.",
			InputSchema: mcpObjectSchema(map[string]any{
				"preset": mcpStringSchema("Environment preset to resolve and apply."),
				"argv":   mcpArgvSchema(),
			}, "preset", "argv"),
			handler: (*App).mcpToolWenvExec,
		},
	}
}

func (a *App) mcpServe(args []string) error {
	if len(args) != 0 {
		return usage("mcp serve", "")
	}
	tools := mcpServeTools()
	index := make(map[string]mcpTool, len(tools))
	for _, tool := range tools {
		index[tool.Name] = tool
	}
	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 0, 64<<10), mcpServeMaxRequestBytes)
	writer := bufio.NewWriter(a.out)
	encoder := json.NewEncoder(writer)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		response, answer := a.mcpServeDispatch(line, tools, index)
		if !answer {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("flush MCP response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

func (a *App) mcpServeDispatch(line []byte, tools []mcpTool, index map[string]mcpTool) (jsonrpcResponse, bool) {
	var request jsonrpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return jsonrpcFailure(nil, -32700, "parse error"), true
	}
	notification := isJSONRPCNotification(request.ID)
	switch request.Method {
	case "initialize":
		return jsonrpcResult(request.ID, map[string]any{
			"protocolVersion": negotiateMCPProtocol(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "bb", "version": Version},
			"instructions":    mcpServeInstructions,
		}), !notification
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return jsonrpcResponse{}, false
	case "ping":
		return jsonrpcResult(request.ID, map[string]any{}), !notification
	case "tools/list":
		return jsonrpcResult(request.ID, map[string]any{"tools": tools}), !notification
	case "resources/list":
		return jsonrpcResult(request.ID, map[string]any{"resources": []any{}}), !notification
	case "prompts/list":
		return jsonrpcResult(request.ID, map[string]any{"prompts": []any{}}), !notification
	case "tools/call":
		if notification {
			return jsonrpcResponse{}, false
		}
		return a.mcpServeCall(request, index), true
	default:
		if notification {
			return jsonrpcResponse{}, false
		}
		return jsonrpcFailure(request.ID, -32601, "unknown method "+safeTerminalText(request.Method)), true
	}
}

func negotiateMCPProtocol(params json.RawMessage) string {
	var requested struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &requested)
	}
	if containsString(mcpServeProtocols, requested.ProtocolVersion) {
		return requested.ProtocolVersion
	}
	return mcpServeLatestProtocol
}

func (a *App) mcpServeCall(request jsonrpcRequest, index map[string]mcpTool) jsonrpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return jsonrpcFailure(request.ID, -32602, "invalid tools/call parameters")
	}
	tool, known := index[params.Name]
	if !known {
		return jsonrpcFailure(request.ID, -32602, "unknown tool "+safeTerminalText(params.Name))
	}
	data, err := tool.handler(a, params.Arguments)
	if err != nil {
		return jsonrpcResult(request.ID, mcpToolFailure(err))
	}
	return jsonrpcResult(request.ID, mcpToolSuccess(data))
}

func mcpToolSuccess(data any) map[string]any {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return mcpToolFailure(fmt.Errorf("encode tool result: %w", err))
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
		"structuredContent": data,
		"isError":           false,
	}
}

func mcpToolFailure(err error) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": safeTerminalText(err.Error())}},
		"isError": true,
	}
}

func decodeMCPArguments[T any](arguments json.RawMessage, into *T) error {
	if len(bytes.TrimSpace(arguments)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return invalid("invalid tool arguments: " + err.Error())
	}
	return nil
}

type mcpSecretService struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

func (a *App) mcpToolSecList(arguments json.RawMessage) (any, error) {
	var input struct {
		Service string `json:"service"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	data, err := a.readSecrets()
	if err != nil {
		return nil, err
	}
	if input.Service != "" {
		fields, ok := data[input.Service]
		if !ok {
			return nil, invalid("secret service not found: " + safeTerminalText(input.Service))
		}
		return mcpSecretService{Name: input.Service, Fields: sortedSecretFields(fields)}, nil
	}
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)
	services := make([]mcpSecretService, 0, len(names))
	for _, name := range names {
		services = append(services, mcpSecretService{Name: name, Fields: sortedSecretFields(data[name])})
	}
	return map[string]any{"services": services}, nil
}

func sortedSecretFields(fields map[string]string) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *App) mcpToolSecRef(arguments json.RawMessage) (any, error) {
	var input struct {
		Service string `json:"service"`
		Field   string `json:"field"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	if !validSecretName(input.Service) {
		return nil, invalid("secret service name is required")
	}
	if input.Field != "" && !validSecretName(input.Field) {
		return nil, invalid("secret field names may contain only letters, digits, dot, underscore, and hyphen")
	}
	data, err := a.readSecrets()
	if err != nil {
		return nil, err
	}
	field, err := resolveSecretField(data, input.Service, input.Field)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"service":   input.Service,
		"field":     field,
		"reference": wenvSecretReferencePrefix + input.Service + "/" + field,
	}, nil
}

type mcpWenvPreset struct {
	Name      string `json:"name"`
	Variables int    `json:"variables"`
}

func (a *App) mcpToolWenvList(arguments json.RawMessage) (any, error) {
	var input struct{}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	store, _, err := a.loadWenv()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(store.Presets))
	for name := range store.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	presets := make([]mcpWenvPreset, 0, len(names))
	for _, name := range names {
		presets = append(presets, mcpWenvPreset{Name: name, Variables: len(store.Presets[name])})
	}
	return map[string]any{"presets": presets}, nil
}

func (a *App) mcpToolWenvShow(arguments json.RawMessage) (any, error) {
	var input struct {
		Preset string `json:"preset"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	vars, err := a.namedWenv(input.Preset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"preset": input.Preset, "variables": mcpWenvPreview(vars)}, nil
}

// mcpWenvPreview reuses the CLI preview rules so the MCP surface can never show
// more than a terminal preview would.
func mcpWenvPreview(vars map[string]string) map[string]string {
	preview := make(map[string]string, len(vars))
	for key, value := range vars {
		preview[key] = wenvPreviewValue(key, value)
	}
	return preview
}

func (a *App) mcpToolWenvSet(arguments json.RawMessage) (any, error) {
	var input struct {
		Preset      string   `json:"preset"`
		Assignments []string `json:"assignments"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	if !presetNameRE.MatchString(input.Preset) {
		return nil, invalid("wenv preset names may contain only letters, digits, dot, underscore, and hyphen")
	}
	if len(input.Assignments) == 0 {
		return nil, invalid("at least one KEY=VALUE assignment is required")
	}
	updates := make(map[string]string, len(input.Assignments))
	for _, assignment := range input.Assignments {
		key, value, err := parseWenvAssignment(assignment)
		if err != nil {
			return nil, err
		}
		updates[key] = value
	}
	var merged map[string]string
	path, err := a.wenvPath()
	if err != nil {
		return nil, err
	}
	err = withFileLock(path, func() error {
		store := wenvStore{Presets: map[string]map[string]string{}}
		if readErr := readJSON(path, &store); readErr != nil {
			return readErr
		}
		if store.Presets == nil {
			store.Presets = map[string]map[string]string{}
		}
		vars := store.Presets[input.Preset]
		if vars == nil {
			vars = map[string]string{}
		}
		for key, value := range updates {
			vars[key] = value
		}
		store.Presets[input.Preset] = vars
		merged = vars
		return writeJSONAtomic(path, store)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"preset": input.Preset, "variables": mcpWenvPreview(merged)}, nil
}

func (a *App) mcpToolSecExec(arguments json.RawMessage) (any, error) {
	var input struct {
		Service string   `json:"service"`
		Argv    []string `json:"argv"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	if err := a.authorizeMCPExec("sec_exec", input.Service, input.Argv); err != nil {
		return nil, err
	}
	data, err := a.readSecrets()
	if err != nil {
		return nil, err
	}
	fields, ok := data[input.Service]
	if !ok {
		return nil, invalid("secret service not found: " + safeTerminalText(input.Service))
	}
	if len(fields) == 0 {
		return nil, invalid("secret service has no fields: " + safeTerminalText(input.Service))
	}
	variables, err := secretEnvironmentVariables(input.Service, fields)
	if err != nil {
		return nil, err
	}
	return a.mcpRunWithEnvironment("sec_exec", input.Service, variables, variables, input.Argv)
}

func (a *App) mcpToolWenvExec(arguments json.RawMessage) (any, error) {
	var input struct {
		Preset string   `json:"preset"`
		Argv   []string `json:"argv"`
	}
	if err := decodeMCPArguments(arguments, &input); err != nil {
		return nil, err
	}
	if err := a.authorizeMCPExec("wenv_exec", input.Preset, input.Argv); err != nil {
		return nil, err
	}
	// Only values that came out of the encrypted store are redaction candidates.
	// Redacting ordinary configuration would make output unreadable for no gain.
	variables, secrets, err := a.wenvEnvironment(input.Preset)
	if err != nil {
		return nil, err
	}
	return a.mcpRunWithEnvironment("wenv_exec", input.Preset, variables, secrets, input.Argv)
}

type mcpExecResult struct {
	Command   string   `json:"command"`
	ExitCode  int      `json:"exit_code"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Truncated bool     `json:"truncated"`
	Redacted  bool     `json:"redacted"`
	Warnings  []string `json:"warnings"`
}

// mcpRunWithEnvironment is the only path by which a secret value leaves the
// store during an MCP session, and it leaves into a child process environment
// rather than into the response. variables are injected; secrets are the subset
// scrubbed back out of the captured output.
// authorizeMCPExec runs before the store is decrypted. A command bb will not run
// is refused without touching a secret at all.
func (a *App) authorizeMCPExec(tool, target string, argv []string) error {
	if len(argv) == 0 {
		return invalid("argv must name a command")
	}
	for _, argument := range argv {
		if argument == "" || strings.ContainsRune(argument, 0) {
			return invalid("argv entries must be non-empty and free of NUL")
		}
	}
	config, err := a.loadMCPServeConfig()
	if err != nil {
		return err
	}
	if len(config.ExecRules) == 0 {
		a.appendMCPServeAudit(tool, target, argv, false, -1)
		return unavailable("no command is allowlisted for MCP execution; run 'bb mcp allow add <command>'")
	}
	if !config.permits(argv) {
		a.appendMCPServeAudit(tool, target, argv, false, -1)
		return mcpExecDenial(config, argv)
	}
	scope, kind := config.ExecServices, "secret service"
	if tool == "wenv_exec" {
		scope, kind = config.ExecPresets, "wenv preset"
	}
	if len(scope) > 0 && !containsString(scope, target) {
		a.appendMCPServeAudit(tool, target, argv, false, -1)
		return invalid(fmt.Sprintf("%s is outside the MCP exec scope: %s", kind, safeTerminalText(target)))
	}
	if _, err := a.lookPath(argv[0]); err != nil {
		return unavailable("command not found on PATH: " + safeTerminalText(argv[0]))
	}
	return nil
}

// mcpExecDenial distinguishes an unlisted command from a listed one invoked with
// arguments outside its prefix, and names the prefixes that would work.
func mcpExecDenial(config mcpServeConfig, argv []string) error {
	rules := config.rulesFor(argv[0])
	if len(rules) == 0 {
		return invalid("command is not on the MCP exec allowlist: " + safeTerminalText(argv[0]))
	}
	allowed := make([]string, 0, len(rules))
	for _, rule := range rules {
		allowed = append(allowed, rule.String())
	}
	return invalid(fmt.Sprintf("arguments are not allowed for %s; allowed: %s",
		safeTerminalText(argv[0]), safeTerminalText(strings.Join(allowed, ", "))))
}

func (a *App) mcpRunWithEnvironment(tool, target string, variables, secrets []secretEnvironmentVariable, argv []string) (any, error) {
	stdout := &cappedBuffer{limit: mcpServeMaxOutputBytes}
	stderr := &cappedBuffer{limit: mcpServeMaxOutputBytes}
	cmd := a.command(argv[0], argv[1:]...)
	cmd.Env = overlaySecretEnvironment(a.env, variables)
	// stdin is deliberately empty: a.in carries the MCP JSON-RPC stream and must
	// never be handed to a child process.
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	exitCode := 0
	timedOut := false
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", safeTerminalText(argv[0]), err)
	}
	timer := time.AfterFunc(mcpServeExecTimeout, func() {
		timedOut = true
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	waitErr := cmd.Wait()
	timer.Stop()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return nil, fmt.Errorf("run %s: %w", safeTerminalText(argv[0]), waitErr)
		}
		exitCode = exitErr.ExitCode()
	}

	values, warnings := redactionValues(secrets)
	outText, outRedacted := redactSecretValues(stdout.String(), values)
	errText, errRedacted := redactSecretValues(stderr.String(), values)
	if timedOut {
		warnings = append(warnings, fmt.Sprintf("command exceeded %s and was killed", mcpServeExecTimeout))
	}
	a.appendMCPServeAudit(tool, target, argv, true, exitCode)
	return mcpExecResult{
		Command:   argv[0],
		ExitCode:  exitCode,
		Stdout:    outText,
		Stderr:    errText,
		Truncated: stdout.truncated || stderr.truncated,
		Redacted:  outRedacted || errRedacted,
		Warnings:  warnings,
	}, nil
}

// redactionValues splits injected values into those long enough to substitute
// safely and those that are reported instead. A one-character value matches
// everywhere, so silently replacing it would destroy the output it is meant to
// protect; saying so is more useful than either extreme.
func redactionValues(secrets []secretEnvironmentVariable) ([]string, []string) {
	values := make([]string, 0, len(secrets))
	warnings := []string{}
	for _, secret := range secrets {
		if len(secret.Value) < mcpServeMinRedactBytes {
			warnings = append(warnings, fmt.Sprintf("%s is shorter than %d bytes and was not redacted from the output", secret.Name, mcpServeMinRedactBytes))
			continue
		}
		values = append(values, secret.Value)
	}
	return values, warnings
}

func redactSecretValues(text string, values []string) (string, bool) {
	if text == "" || len(values) == 0 {
		return text, false
	}
	ordered := append([]string{}, values...)
	// Longest first so a value that contains another collapses predictably.
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	redacted := false
	for _, value := range ordered {
		if !strings.Contains(text, value) {
			continue
		}
		text = strings.ReplaceAll(text, value, "***")
		redacted = true
	}
	return text, redacted
}

// cappedBuffer keeps a runaway command from filling the response, and records
// that it did so instead of silently shortening the output.
type cappedBuffer struct {
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		if len(p) > 0 {
			c.truncated = true
		}
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// appendMCPServeAudit records that a value was used, never which value. argv
// beyond the command name is omitted: an agent can put anything in there.
func (a *App) appendMCPServeAudit(tool, target string, argv []string, allowed bool, exitCode int) {
	_, state, err := a.paths()
	if err != nil {
		return
	}
	entry := map[string]any{
		"time":      a.now().UTC().Format(time.RFC3339),
		"tool":      tool,
		"target":    target,
		"command":   argv[0],
		"argv_size": len(argv),
		"allowed":   allowed,
	}
	if allowed {
		entry["exit_code"] = exitCode
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := filepath.Join(state, "mcp-serve-audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}
