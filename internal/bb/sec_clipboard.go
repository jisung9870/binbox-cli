package bb

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultClipboardClearSeconds = 30
	clipboardClearEnv            = "BB_SEC_CLIPBOARD_CLEAR"
	clipboardClearCommand        = "__clipboard-clear"
)

type clipboardTool struct {
	copy, paste []string
}

var clipboardTools = []clipboardTool{
	{copy: []string{"pbcopy"}, paste: []string{"pbpaste"}},
	{copy: []string{"clip.exe"}, paste: []string{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"}},
	{copy: []string{"wl-copy"}, paste: []string{"wl-paste", "--no-newline"}},
	{copy: []string{"xclip", "-selection", "clipboard"}, paste: []string{"xclip", "-selection", "clipboard", "-o"}},
}

func (a *App) clipboardTool() (clipboardTool, error) {
	for _, tool := range clipboardTools {
		if _, e := a.lookPath(tool.copy[0]); e == nil {
			return tool, nil
		}
	}
	return clipboardTool{}, unavailable("no clipboard command found")
}

func (a *App) writeClipboard(value string) error {
	tool, e := a.clipboardTool()
	if e != nil {
		return e
	}
	cmd := a.command(tool.copy[0], tool.copy[1:]...)
	cmd.Env = a.env
	cmd.Stdin = strings.NewReader(value)
	cmd.Stderr = a.err
	return cmd.Run()
}

func (a *App) readClipboard() ([]byte, error) {
	tool, e := a.clipboardTool()
	if e != nil {
		return nil, e
	}
	if _, e := a.lookPath(tool.paste[0]); e != nil {
		return nil, unavailable(tool.paste[0] + " is required to read the clipboard")
	}
	cmd := a.command(tool.paste[0], tool.paste[1:]...)
	cmd.Env = a.env
	var out bytes.Buffer
	cmd.Stdout = &out
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("read clipboard: %w", e)
	}
	if out.Len() > maxSecretValueBytes {
		return nil, invalid("clipboard value exceeds 16 MiB")
	}
	return out.Bytes(), nil
}

// clipboardDigest ignores trailing line endings because some paste tools
// (Get-Clipboard) append CRLF to what was copied.
func clipboardDigest(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(bytes.TrimRight(b, "\r\n")))
}

// clearClipboardIfUnchanged empties the clipboard only while it still holds the
// secret, so anything the operator copied afterwards is left alone.
func (a *App) clearClipboardIfUnchanged(digest string) error {
	current, e := a.readClipboard()
	if e != nil {
		return e
	}
	if clipboardDigest(current) != digest {
		return nil
	}
	return a.writeClipboard("")
}

func (a *App) clipboardClearSeconds() int {
	raw := a.getenv(clipboardClearEnv)
	if raw == "" {
		return defaultClipboardClearSeconds
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 {
		return defaultClipboardClearSeconds
	}
	return n
}

// scheduleClipboardClear starts a detached bb that clears the clipboard after
// the delay. Only a SHA-256 digest of the value reaches the child, through a
// pipe rather than its environment or arguments.
func (a *App) scheduleClipboardClear(value string, seconds int) error {
	self, e := os.Executable()
	if e != nil {
		return e
	}
	r, w, e := os.Pipe()
	if e != nil {
		return e
	}
	defer r.Close()
	if _, e := io.WriteString(w, clipboardDigest([]byte(value))); e != nil {
		w.Close()
		return e
	}
	w.Close()
	cmd := a.command(self, "sec", clipboardClearCommand, strconv.Itoa(seconds))
	cmd.Env = a.env
	cmd.Stdin = r
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e := cmd.Start(); e != nil {
		return e
	}
	return cmd.Process.Release()
}

func (a *App) secClipboardClear(args []string) error {
	if len(args) != 1 {
		return usage("sec "+clipboardClearCommand, "<seconds>")
	}
	seconds, e := strconv.Atoi(args[0])
	raw, readErr := io.ReadAll(io.LimitReader(a.in, 128))
	digest := strings.TrimSpace(string(raw))
	if e != nil || seconds < 0 || readErr != nil || len(digest) != sha256.Size*2 {
		return invalid("internal clipboard clear needs a delay and a digest on stdin")
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	return a.clearClipboardIfUnchanged(digest)
}
