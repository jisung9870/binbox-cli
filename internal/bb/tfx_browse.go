package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// bb tfx browse reads a Terraform plan. It runs `terraform show -json` against
// an existing plan file and nothing else: it never applies, never writes state,
// and never rewrites the plan. The account-bound apply and destroy safeguards
// are untouched because this path does not reach them.
//
// Values are rendered through the plan's own sensitivity markers, so a plan can
// be read without printing a secret into the terminal or into search metadata.

const (
	tfxSensitiveValue = "(sensitive)"
	tfxUnknownValue   = "(known after apply)"
	// tfxValueLimit caps one rendered side of an attribute change. Plans carry
	// whole policy documents; this is a review view, and `terraform show -json`
	// remains the lossless source.
	tfxValueLimit = 240
)

type tfxAttributeChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type tfxResourceSummary struct {
	Address    string               `json:"address"`
	Action     string               `json:"action"`
	Changed    int                  `json:"changed_attributes"`
	Review     bool                 `json:"needs_review"`
	Attributes []tfxAttributeChange `json:"attributes"`
}

// tfxPlanRow is the table shape: the same summary without the nested attribute
// list, which does not belong in a flat row.
type tfxPlanRow struct {
	Action  string `json:"action"`
	Address string `json:"address"`
	Changed int    `json:"changed_attributes"`
	Review  string `json:"review"`
}

// tfxMarked reports whether a path, or any block containing it, is flagged in
// one of the plan's parallel marker structures. Terraform may mark a whole
// block sensitive instead of each value inside it.
func tfxMarked(marker any, path string) bool {
	if flag, ok := marker.(bool); ok && flag {
		return true
	}
	parts := strings.Split(path, ".")
	for size := len(parts); size > 0; size-- {
		if flag, ok := valueAtPath(marker, strings.Join(parts[:size], ".")).(bool); ok && flag {
			return true
		}
	}
	return false
}

func tfxScalarText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "(unrenderable)"
		}
		return string(encoded)
	}
}

func tfxRenderValue(value, sensitive, unknown any, path string) string {
	switch {
	case tfxMarked(unknown, path):
		return tfxUnknownValue
	case tfxMarked(sensitive, path):
		return tfxSensitiveValue
	}
	text := safeTerminalText(tfxScalarText(valueAtPath(value, path)))
	if len(text) > tfxValueLimit {
		text = text[:tfxValueLimit] + "…"
	}
	return text
}

// tfxActionLabel names a change the way an operator scans for it. The words are
// searchable, unlike Terraform's +/-/~ symbols.
func tfxActionLabel(actions []string) string {
	create, destroy := false, false
	for _, action := range actions {
		switch action {
		case "create":
			create = true
		case "delete":
			destroy = true
		}
	}
	switch {
	case create && destroy:
		return "replace"
	case destroy:
		return "destroy"
	case create:
		return "create"
	case len(actions) == 1:
		return actions[0]
	case len(actions) == 0:
		return "unknown"
	default:
		return strings.Join(actions, "+")
	}
}

// tfxActionRank orders the list by blast radius so the changes that can lose
// data are never below the fold.
func tfxActionRank(action string) int {
	switch action {
	case "destroy":
		return 0
	case "replace":
		return 1
	case "update":
		return 2
	case "create":
		return 3
	default:
		return 4
	}
}

func summarizeTFXPlan(data []byte, rules tfxReviewRules) ([]tfxResourceSummary, error) {
	changes, err := parseTFXPlanChanges(data)
	if err != nil {
		return nil, err
	}
	summaries := make([]tfxResourceSummary, 0, len(changes))
	for _, resource := range changes {
		if tfxUnchanged(resource.Change.Actions) {
			continue
		}
		differences := tfxChangedPaths(resource)
		attributes := make([]tfxAttributeChange, 0, len(differences))
		for _, path := range differences {
			attributes = append(attributes, tfxAttributeChange{
				Path:   safeTerminalText(path),
				Before: tfxRenderValue(resource.Change.Before, resource.Change.BeforeSensitive, nil, path),
				After:  tfxRenderValue(resource.Change.After, resource.Change.AfterSensitive, resource.Change.AfterUnknown, path),
			})
		}
		summaries = append(summaries, tfxResourceSummary{
			Address:    safeTerminalText(resource.Address),
			Action:     tfxActionLabel(resource.Change.Actions),
			Changed:    len(attributes),
			Review:     tfxNeedsReview(resource, differences, rules),
			Attributes: attributes,
		})
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		left, right := tfxActionRank(summaries[i].Action), tfxActionRank(summaries[j].Action)
		if left != right {
			return left < right
		}
		return summaries[i].Address < summaries[j].Address
	})
	return summaries, nil
}

func (a *App) tfxBrowse(args []string) error {
	args, asJSON := takeFlag(args, "--json")
	if len(args) > 1 {
		return usage("tfx browse", "[plan] [--json]")
	}
	plan := a.getenv("TFPLAN_FILE")
	if plan == "" {
		plan = "tfplan"
	}
	if len(args) == 1 {
		plan = args[0]
	}
	if _, err := os.Stat(plan); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plan file not found: %s (run 'bb tfx plan' first)", plan)
		}
		return fmt.Errorf("inspect plan file %s: %w", plan, err)
	}
	if _, err := a.lookPath("terraform"); err != nil {
		return unavailable("terraform is not installed; install terraform to use bb tfx browse")
	}

	terraform := a.command("terraform", "show", "-json", plan)
	var shown, failed bytes.Buffer
	terraform.Env, terraform.Stdout, terraform.Stderr = a.env, &shown, &failed
	if err := terraform.Run(); err != nil {
		_, _ = a.err.Write(failed.Bytes())
		return fmt.Errorf("terraform show -json %s: %w", plan, err)
	}

	rules, err := readTFXReviewRules(".")
	if err != nil {
		return err
	}
	summaries, err := summarizeTFXPlan(shown.Bytes(), rules)
	if err != nil {
		return err
	}

	if asJSON {
		destroy, review := tfxPlanCounts(summaries)
		return printEnvelope(a.out, map[string]any{
			"plan":         plan,
			"changes":      len(summaries),
			"destroy":      destroy,
			"needs_review": review,
			"resources":    summaries,
		}, nil)
	}
	if len(summaries) == 0 {
		_, err := fmt.Fprintln(a.out, "Plan has no changes.")
		return err
	}
	// Without a usable terminal there is no browsing to do and no value to
	// return, so the same reading is written as a table instead of prompting.
	if !a.useBubbleSelector() {
		return printHuman(a.out, tfxPlanRows(summaries))
	}
	return a.browseTFXPlan(plan, summaries)
}

func tfxPlanCounts(summaries []tfxResourceSummary) (destroy, review int) {
	for _, resource := range summaries {
		if resource.Action == "destroy" || resource.Action == "replace" {
			destroy++
		}
		if resource.Review {
			review++
		}
	}
	return destroy, review
}

func tfxPlanRows(summaries []tfxResourceSummary) []tfxPlanRow {
	rows := make([]tfxPlanRow, 0, len(summaries))
	for _, resource := range summaries {
		mark := "-"
		if resource.Review {
			mark = "yes"
		}
		rows = append(rows, tfxPlanRow{
			Action:  resource.Action,
			Address: resource.Address,
			Changed: resource.Changed,
			Review:  mark,
		})
	}
	return rows
}

func (a *App) browseTFXPlan(plan string, summaries []tfxResourceSummary) error {
	byAddress := make(map[string]tfxResourceSummary, len(summaries))
	choices := make([]selectChoice, 0, len(summaries))
	for _, resource := range summaries {
		byAddress[resource.Address] = resource
		description := fmt.Sprintf("%d changed", resource.Changed)
		if resource.Review {
			description += " · needs review"
		}
		choices = append(choices, selectChoice{
			Value: resource.Address,
			// The action is padded so the addresses line up while staying
			// searchable as a word.
			Label:       fmt.Sprintf("%-7s  %s", resource.Action, resource.Address),
			Description: description,
			SearchText:  resource.Action + " " + resource.Address,
		})
	}

	destroy, review := tfxPlanCounts(summaries)
	root := selectStage{
		Prompt:  "resource change",
		Title:   fmt.Sprintf("%s · %d changes · %d destroy · %d review", filepath.Base(plan), len(summaries), destroy, review),
		Choices: choices,
	}
	next := func(path []string) *selectStage {
		if len(path) != 1 {
			return nil
		}
		resource := byAddress[path[0]]
		attributes := make([]selectChoice, 0, len(resource.Attributes))
		for _, attribute := range resource.Attributes {
			attributes = append(attributes, selectChoice{
				Value:       attribute.Path,
				Label:       attribute.Path,
				Description: attribute.Before + " → " + attribute.After,
				SearchText:  attribute.Before + " " + attribute.After,
			})
		}
		if len(attributes) == 0 {
			attributes = append(attributes, selectChoice{
				Value:       resource.Address,
				Label:       "No attribute-level differences",
				Description: "the plan records this change without differing values",
			})
		}
		return &selectStage{
			Prompt:   "attribute",
			Title:    resource.Action + "  " + resource.Address,
			Choices:  attributes,
			ReadOnly: true,
		}
	}

	_, err := a.selectStages(root, next)
	return err
}
