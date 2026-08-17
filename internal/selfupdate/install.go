package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const maximumBinarySize = 128 << 20

type CommandRunner func(context.Context, string, []string, []string, io.Writer, io.Writer) error

type Installer struct {
	Executable string
	GoBinary   string
	Command    string
	Stdout     io.Writer
	Stderr     io.Writer
	Run        CommandRunner
	Verify     func(string, string) error
}

func (installer Installer) Install(ctx context.Context, target string) error {
	if _, ok := parseVersion(target); !ok {
		return fmt.Errorf("invalid target version %q", target)
	}
	if runtime.GOOS == "windows" {
		return errors.New("Windows cannot replace a running executable; use `go install " + CommandPath + "@" + target + "`")
	}
	if installer.Command == "" {
		installer.Command = CommandPath
	}
	if installer.Stdout == nil {
		installer.Stdout = io.Discard
	}
	if installer.Stderr == nil {
		installer.Stderr = io.Discard
	}
	if installer.Run == nil {
		installer.Run = runCommand
	}
	if installer.Verify == nil {
		installer.Verify = verifyBinary
	}
	if installer.Executable == "" {
		path, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate current executable: %w", err)
		}
		installer.Executable = path
	}
	destination, err := filepath.EvalSymlinks(installer.Executable)
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("current executable is not a regular file")
	}
	if installer.GoBinary == "" {
		installer.GoBinary, err = exec.LookPath("go")
		if err != nil {
			return errors.New("updating gack requires Go on PATH; install Go or use your package manager")
		}
	}

	directory, err := os.MkdirTemp("", "gack-update-*")
	if err != nil {
		return fmt.Errorf("create update workspace: %w", err)
	}
	defer os.RemoveAll(directory)
	environment := replaceEnvironment(os.Environ(), "GOBIN", directory)
	arguments := []string{"install", "-trimpath", installer.Command + "@" + target}
	if err := installer.Run(ctx, installer.GoBinary, arguments, environment, installer.Stdout, installer.Stderr); err != nil {
		return fmt.Errorf("build %s: %w", target, err)
	}
	candidate := filepath.Join(directory, executableName("gack"))
	if err := installer.Verify(candidate, target); err != nil {
		return fmt.Errorf("verify downloaded update: %w", err)
	}
	if err := replaceExecutable(destination, candidate, info.Mode().Perm()); err != nil {
		return fmt.Errorf("replace %s: %w", destination, err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, arguments, environment []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func verifyBinary(path, target string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBinarySize {
		return fmt.Errorf("candidate has invalid size or type")
	}
	command := exec.Command(path, "--version")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("candidate did not start: %w", err)
	}
	fields := strings.Fields(output.String())
	if len(fields) != 2 || fields[0] != "gack" || fields[1] != target {
		return fmt.Errorf("candidate reported %q, want gack %s", strings.TrimSpace(output.String()), target)
	}
	return nil
}

func replaceExecutable(destination, candidate string, permissions os.FileMode) error {
	source, err := os.Open(candidate)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBinarySize {
		return errors.New("candidate is not a bounded regular file")
	}

	temporary, err := os.CreateTemp(filepath.Dir(destination), ".gack-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(permissions); err != nil {
		temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, io.LimitReader(source, maximumBinarySize+1)); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
