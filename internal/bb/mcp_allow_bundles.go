package bb

import (
	"sort"
	"strings"
)

// mcpAllowBundle is a named set of argument-restricted rules for a CLI an agent
// commonly drives with a token. Bundles list read-only subcommands only; they
// are a starting point, and each rule can still be removed on its own.
type mcpAllowBundle struct {
	Description string
	Rules       [][]string
	// DenyArgs are host- or repository-selecting flags refused anywhere in
	// argv; with DenyURLs the CLI also cannot be handed a URL on another host.
	DenyArgs []string
	DenyURLs bool
}

// auth status subcommands are left out on purpose: "--show-token" prints every
// token the CLI holds, not only the injected one that redaction knows about.
var mcpAllowBundles = map[string]mcpAllowBundle{
	"gh-read": {
		Description: "GitHub CLI: view pull requests, issues, runs, releases and the repository of the working directory",
		Rules: [][]string{
			{"gh", "issue", "list"}, {"gh", "issue", "view"},
			{"gh", "pr", "checks"}, {"gh", "pr", "diff"}, {"gh", "pr", "list"}, {"gh", "pr", "view"},
			{"gh", "release", "list"}, {"gh", "release", "view"},
			{"gh", "repo", "view"},
			{"gh", "run", "list"}, {"gh", "run", "view"},
		},
		DenyArgs: []string{"-R", "--repo", "--hostname"},
		DenyURLs: true,
	},
	"glab-read": {
		Description: "GitLab CLI: view merge requests, issues, pipelines, releases and the repository of the working directory",
		Rules: [][]string{
			{"glab", "ci", "get"}, {"glab", "ci", "list"},
			{"glab", "issue", "list"}, {"glab", "issue", "view"},
			{"glab", "mr", "diff"}, {"glab", "mr", "list"}, {"glab", "mr", "view"},
			{"glab", "release", "list"}, {"glab", "release", "view"},
			{"glab", "repo", "view"},
		},
		DenyArgs: []string{"-R", "--repo", "--hostname"},
		DenyURLs: true,
	},
	"aws-identity": {
		Description: "AWS CLI: confirm which identity a credential belongs to",
		Rules: [][]string{
			{"aws", "sts", "get-caller-identity"},
		},
		DenyArgs: []string{"--endpoint-url"},
		DenyURLs: true,
	},
}

func mcpAllowBundleRules(name string) ([]mcpExecRule, bool) {
	bundle, ok := mcpAllowBundles[name]
	if !ok {
		return nil, false
	}
	rules := make([]mcpExecRule, 0, len(bundle.Rules))
	for _, argv := range bundle.Rules {
		rules = append(rules, mcpExecRule{
			Command:  argv[0],
			Args:     append([]string{}, argv[1:]...),
			DenyArgs: append([]string{}, bundle.DenyArgs...),
			DenyURLs: bundle.DenyURLs,
		})
	}
	return rules, true
}

func (a *App) mcpAllowBundles(jsonMode bool) error {
	names := make([]string, 0, len(mcpAllowBundles))
	for name := range mcpAllowBundles {
		names = append(names, name)
	}
	sort.Strings(names)
	type listed struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Rules       []string `json:"rules"`
	}
	bundles := make([]listed, 0, len(names))
	for _, name := range names {
		bundle := mcpAllowBundles[name]
		rules := make([]string, 0, len(bundle.Rules))
		for _, argv := range bundle.Rules {
			rules = append(rules, strings.Join(argv, " "))
		}
		description := bundle.Description
		if len(bundle.DenyArgs) > 0 {
			description += "; refuses " + strings.Join(bundle.DenyArgs, " ")
			if bundle.DenyURLs {
				description += " and URLs"
			}
		}
		bundles = append(bundles, listed{Name: name, Description: description, Rules: rules})
	}
	if jsonMode {
		return printEnvelope(a.out, map[string]any{"bundles": bundles}, nil)
	}
	for _, bundle := range bundles {
		if _, err := a.out.Write([]byte(bundle.Name + "  " + bundle.Description + "\n    " + strings.Join(bundle.Rules, "\n    ") + "\n")); err != nil {
			return err
		}
	}
	return nil
}
