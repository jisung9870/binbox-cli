package awsbrowser

import (
	"fmt"
	"strings"
)

type IntentKind string

const (
	IntentOpen    IntentKind = "open"
	IntentRefresh IntentKind = "refresh"
	IntentSearch  IntentKind = "cross-profile-search"
)

type Intent struct {
	Kind    IntentKind
	Target  string
	Profile string
	Region  string
}

type IntentResultMsg struct {
	Intent Intent
	Err    error
}

func (m IntentResultMsg) Error() string {
	if m.Err == nil {
		return ""
	}
	return safeIntentText(fmt.Sprintf("%s: %v", m.Intent.Target, m.Err))
}

func safeIntentText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
}
