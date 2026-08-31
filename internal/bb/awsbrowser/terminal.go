package awsbrowser

import "io"

const (
	MinimumWidth  = 40
	MinimumHeight = 12

	ScopedQueryGuidance = "bb: aws browse requires an interactive TTY; use a scoped query:\n" +
		"  bb aws query ec2 instances --profile dev --region ap-northeast-2 --json\n" +
		"  bb aws query ami ami-0123456789abcdef0 --scope all --json\n" +
		"  bb aws query domain api.example.com --scope all --json\n"
	MinimumSizeMessage = "Terminal too small (need 40x12).\nResize or rerun with BB_SELECTOR=plain."
)

type Terminal struct {
	In                  io.Reader
	Err                 io.Writer
	StdinTTY, StderrTTY bool
	Width, Height       int
}

func (t Terminal) Interactive() bool { return t.StdinTTY && t.StderrTTY }

func (t Terminal) Small() bool {
	return t.Width > 0 && t.Height > 0 && (t.Width < MinimumWidth || t.Height < MinimumHeight)
}
