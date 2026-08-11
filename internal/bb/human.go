package bb

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

func printHuman(w io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return err
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if err := renderHuman(table, normalized, ""); err != nil {
		return err
	}
	return table.Flush()
}

func renderHuman(w io.Writer, value any, indent string) error {
	switch typed := value.(type) {
	case map[string]any:
		return renderHumanMap(w, typed, indent)
	case []any:
		return renderHumanSlice(w, typed, indent)
	default:
		text, _ := humanScalar(typed)
		_, err := fmt.Fprintln(w, indent+text)
		return err
	}
}

func renderHumanMap(w io.Writer, values map[string]any, indent string) error {
	if len(values) == 0 {
		_, err := fmt.Fprintln(w, indent+"No details.")
		return err
	}
	keys := sortedHumanKeys(values)
	for _, key := range keys {
		if text, ok := humanScalar(values[key]); ok {
			if _, err := fmt.Fprintf(w, "%s%s:\t%s\n", indent, humanLabel(key), text); err != nil {
				return err
			}
		}
	}
	for _, key := range keys {
		if _, ok := humanScalar(values[key]); ok {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s%s:\n", indent, humanLabel(key)); err != nil {
			return err
		}
		if err := renderHuman(w, values[key], indent+"  "); err != nil {
			return err
		}
	}
	return nil
}

func renderHumanSlice(w io.Writer, values []any, indent string) error {
	if len(values) == 0 {
		_, err := fmt.Fprintln(w, indent+"No results.")
		return err
	}
	rows := make([]map[string]string, len(values))
	columns := map[string]bool{}
	for index, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			for _, item := range values {
				text, scalar := humanScalar(item)
				if scalar {
					if _, err := fmt.Fprintln(w, indent+"- "+text); err != nil {
						return err
					}
					continue
				}
				if _, err := fmt.Fprintln(w, indent+"-"); err != nil {
					return err
				}
				if err := renderHuman(w, item, indent+"  "); err != nil {
					return err
				}
			}
			return nil
		}
		row := map[string]string{}
		flattenHumanRow("", object, row)
		rows[index] = row
		for key := range row {
			columns[key] = true
		}
	}
	ordered := make([]string, 0, len(columns))
	for column := range columns {
		ordered = append(ordered, column)
	}
	sortHumanKeys(ordered)
	for index, column := range ordered {
		if index > 0 {
			fmt.Fprint(w, "\t")
		}
		fmt.Fprint(w, indent+humanLabel(column))
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	for _, row := range rows {
		for index, column := range ordered {
			if index > 0 {
				fmt.Fprint(w, "\t")
			}
			fmt.Fprint(w, indent+row[column])
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

func flattenHumanRow(prefix string, values map[string]any, row map[string]string) {
	for key, value := range values {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		if text, ok := humanScalar(value); ok {
			row[name] = text
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			flattenHumanRow(name, nested, row)
			continue
		}
		row[name] = "(details)"
	}
}

func humanScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "-", true
	case string:
		return safeTerminalText(typed), true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case []any:
		items := make([]string, len(typed))
		for index, item := range typed {
			text, ok := humanScalar(item)
			if !ok {
				return "", false
			}
			items[index] = text
		}
		return strings.Join(items, ", "), true
	default:
		return "", false
	}
}

func sortedHumanKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortHumanKeys(keys)
	return keys
}

func sortHumanKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		left, right := humanKeyRank(keys[i]), humanKeyRank(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
}

func humanKeyRank(key string) int {
	parts := strings.Split(key, ".")
	leaf := parts[len(parts)-1]
	for index, preferred := range []string{"name", "id", "status", "state", "available", "listening", "exists", "path", "project", "type", "outcome", "time", "started_at", "added_at", "stopped_at"} {
		if leaf == preferred {
			return index
		}
	}
	return 100
}

func humanLabel(value string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", ".", " ").Replace(value))
	for index, word := range words {
		if acronym, ok := map[string]string{
			"api": "API", "aws": "AWS", "id": "ID", "json": "JSON", "mcp": "MCP",
			"pid": "PID", "sbom": "SBOM", "sha256": "SHA-256", "url": "URL", "xdg": "XDG",
		}[strings.ToLower(word)]; ok {
			words[index] = acronym
		} else if word != "" {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
