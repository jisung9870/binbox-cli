package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlainStartupAndQuitAreZeroCall(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("quit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 0 {
		t.Fatalf("startup intents=%+v", dispatcher.intents)
	}
	for _, want := range []string{"AWS Browser · READ ONLY", "1  EC2 Instances", "2  Route 53", "3  IAM Roles", "4  Cross-profile search", "open <n>|back|refresh|quit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain missing %q:\n%s", want, out.String())
		}
	}
}

func TestPlainEOFReturnsBeforeOutput(t *testing.T) {
	var out bytes.Buffer
	err := (Plain{}).Run(context.Background(), Terminal{In: strings.NewReader(""), Err: &out}, Config{})
	if !errors.Is(err, ErrNoInput) || out.Len() != 0 {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestPlainOpenUsesIntentSeam(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 4\nquit\n"), Err: &out}, Config{Profile: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Kind != IntentSearch || dispatcher.intents[0].Target != "cross-profile-search" {
		t.Fatalf("intents=%+v", dispatcher.intents)
	}
}
