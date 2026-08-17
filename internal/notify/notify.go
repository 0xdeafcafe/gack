// Package notify provides the tiny native-notification boundary used by the
// TUI. It intentionally shells out to tools already present on the host so
// gack does not need a resident GUI framework or another runtime.
package notify

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

var ErrUnsupported = errors.New("native notifications are unsupported on this platform")

type Runner func(context.Context, string, ...string) error

type Sender struct {
	GOOS string
	Run  Runner
}

func Default() Sender {
	return Sender{GOOS: runtime.GOOS, Run: runCommand}
}

func (sender Sender) Send(ctx context.Context, title, body string) error {
	if sender.GOOS == "" {
		sender.GOOS = runtime.GOOS
	}
	if sender.Run == nil {
		sender.Run = runCommand
	}
	title = clean(title, 80)
	body = clean(body, 220)
	switch sender.GOOS {
	case "darwin":
		script := `display notification "` + appleScriptString(body) + `" with title "` + appleScriptString(title) + `"`
		return sender.Run(ctx, "osascript", "-e", script)
	case "linux":
		return sender.Run(ctx, "notify-send", "--app-name=gack", title, body)
	default:
		return ErrUnsupported
	}
}

func runCommand(ctx context.Context, name string, arguments ...string) error {
	return exec.CommandContext(ctx, name, arguments...).Run()
}

func clean(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if len(characters) > limit {
		value = string(characters[:limit-1]) + "…"
	}
	return value
}

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
