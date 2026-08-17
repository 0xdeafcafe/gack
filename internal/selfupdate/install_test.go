package selfupdate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerBuildsIntoTemporaryGOBINAndReplacesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("running Windows executables cannot be replaced in place")
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "gack")
	if err := os.WriteFile(destination, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(_ context.Context, name string, arguments, environment []string, _, _ io.Writer) error {
		if name != "fake-go" || strings.Join(arguments, " ") != "install -trimpath "+CommandPath+"@v0.4.0" {
			t.Fatalf("unexpected command: %s %v", name, arguments)
		}
		gobin := ""
		for _, entry := range environment {
			if strings.HasPrefix(entry, "GOBIN=") {
				gobin = strings.TrimPrefix(entry, "GOBIN=")
			}
		}
		return os.WriteFile(filepath.Join(gobin, "gack"), []byte("new binary"), 0o755)
	}
	installer := Installer{
		Executable: destination,
		GoBinary:   "fake-go",
		Run:        run,
		Verify: func(path, target string) error {
			if target != "v0.4.0" || filepath.Base(path) != "gack" {
				t.Fatalf("verify(%q, %q)", path, target)
			}
			return nil
		},
	}
	if err := installer.Install(context.Background(), "v0.4.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary" {
		t.Fatalf("updated executable = %q", data)
	}
}

func TestInstallerRejectsInvalidTargetBeforeRunningCommands(t *testing.T) {
	err := (Installer{}).Install(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "invalid target version") {
		t.Fatalf("unexpected error: %v", err)
	}
}
