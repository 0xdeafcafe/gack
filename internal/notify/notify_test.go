package notify

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinNotificationEscapesAndBoundsContent(t *testing.T) {
	var name string
	var arguments []string
	sender := Sender{GOOS: "darwin", Run: func(_ context.Context, command string, values ...string) error {
		name, arguments = command, values
		return nil
	}}
	if err := sender.Send(context.Background(), `A "workspace"`, "line one\nline two "+strings.Repeat("x", 300)); err != nil {
		t.Fatal(err)
	}
	if name != "osascript" || len(arguments) != 2 || arguments[0] != "-e" || !strings.Contains(arguments[1], `A \"workspace\"`) || strings.Contains(arguments[1], "\n") || len([]rune(arguments[1])) > 360 {
		t.Fatalf("notification command = %q %#v", name, arguments)
	}
}

func TestLinuxNotificationUsesArgumentBoundary(t *testing.T) {
	var got []string
	sender := Sender{GOOS: "linux", Run: func(_ context.Context, name string, values ...string) error {
		got = append([]string{name}, values...)
		return nil
	}}
	if err := sender.Send(context.Background(), "Title", "$(unsafe)"); err != nil {
		t.Fatal(err)
	}
	want := []string{"notify-send", "--app-name=gack", "Title", "$(unsafe)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notification arguments = %#v, want %#v", got, want)
	}
}

func TestUnsupportedNotificationPlatform(t *testing.T) {
	if err := (Sender{GOOS: "plan9"}).Send(context.Background(), "title", "body"); err != ErrUnsupported {
		t.Fatalf("Send() error = %v", err)
	}
}
