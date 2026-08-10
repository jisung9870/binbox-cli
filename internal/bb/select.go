package bb

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type selectChoice struct{ Value, Label string }

// selectOne is the dependency-free interactive selector shared by bb commands.
// UI goes to stderr so stdout can remain a machine/eval-safe data channel.
func (a *App) selectOne(prompt string, choices []selectChoice) (string, error) {
	if len(choices) == 0 {
		return "", unavailable("no selectable items")
	}
	for i, choice := range choices {
		fmt.Fprintf(a.err, "  %d. %s\n", i+1, choice.Label)
	}
	fmt.Fprintf(a.err, "%s [1-%d, name, Enter=cancel]: ", prompt, len(choices))
	answer, err := bufio.NewReader(a.in).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if err != nil && answer == "" {
		return "", fmt.Errorf("read selection: %w", err)
	}
	if answer == "" {
		return "", nil
	}
	if n, conv := strconv.Atoi(answer); conv == nil && n >= 1 && n <= len(choices) {
		return choices[n-1].Value, nil
	}
	for _, choice := range choices {
		if answer == choice.Value {
			return choice.Value, nil
		}
	}
	return "", invalid(fmt.Sprintf("unknown selection %q", answer))
}
