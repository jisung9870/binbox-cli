package bb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const SchemaVersion = 1

const (
	ExitOperational           = 1
	ExitInvalidInvocation     = 2
	ExitCapabilityUnavailable = 3
)

type envelope struct {
	SchemaVersion int            `json:"schema_version"`
	OK            bool           `json:"ok"`
	Data          any            `json:"data"`
	Warnings      []string       `json:"warnings"`
	Error         *envelopeError `json:"error"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CommandError struct {
	Code     string
	Message  string
	Exit     int
	Reported bool
	Cause    error
}

func (e *CommandError) Error() string { return e.Message }
func (e *CommandError) Unwrap() error { return e.Cause }

func commandError(err error) *CommandError {
	var target *CommandError
	if errors.As(err, &target) {
		return target
	}
	return &CommandError{Code: "operational_error", Message: err.Error(), Exit: ExitOperational, Cause: err}
}

func invalid(message string) error {
	return &CommandError{Code: "invalid_invocation", Message: message, Exit: ExitInvalidInvocation}
}

func unavailable(message string) error {
	return &CommandError{Code: "capability_unavailable", Message: message, Exit: ExitCapabilityUnavailable}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return commandError(err).Exit
}

func Reported(err error) bool {
	if err == nil {
		return false
	}
	return commandError(err).Reported
}

func printJSON(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

func printEnvelope(w io.Writer, data any, warnings []string) error {
	if warnings == nil {
		warnings = []string{}
	}
	return printJSON(w, envelope{SchemaVersion: SchemaVersion, OK: true, Data: data, Warnings: warnings})
}

func printErrorEnvelope(w io.Writer, err *CommandError) error {
	return printJSON(w, envelope{
		SchemaVersion: SchemaVersion,
		OK:            false,
		Data:          nil,
		Warnings:      []string{},
		Error:         &envelopeError{Code: err.Code, Message: err.Message},
	})
}

func takeFlag(args []string, flag string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		out = append(out, arg)
	}
	return out, found
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func jsonRequested(args []string) bool {
	if len(args) > 0 && args[0] == "run" {
		if len(args) > 1 && args[1] == "--json" {
			return true
		}
		if len(args) > 2 && (args[1] == "list" || args[1] == "show" || args[1] == "export") {
			return hasFlag(args[2:], "--json")
		}
		return false
	}
	return hasFlag(args, "--json")
}

func helpRequested(args []string) bool {
	return hasFlag(args, "--help") || hasFlag(args, "-h")
}

func usage(command, synopsis string) error {
	return invalid(fmt.Sprintf("usage: bb %s %s", command, synopsis))
}
