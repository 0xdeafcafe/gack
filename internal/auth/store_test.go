package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCredentialNeedsRefresh(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		credential Credential
		want       bool
	}{
		{"no refresh token", Credential{ExpiresAt: now}, false},
		{"no expiry", Credential{RefreshToken: "refresh"}, false},
		{"outside window", Credential{RefreshToken: "refresh", ExpiresAt: now.Add(5*time.Minute + time.Nanosecond)}, false},
		{"at window boundary", Credential{RefreshToken: "refresh", ExpiresAt: now.Add(5 * time.Minute)}, true},
		{"inside window", Credential{RefreshToken: "refresh", ExpiresAt: now.Add(time.Minute)}, true},
		{"expired", Credential{RefreshToken: "refresh", ExpiresAt: now.Add(-time.Minute)}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.credential.NeedsRefresh(now); got != test.want {
				t.Fatalf("NeedsRefresh() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestFileStoreLifecycleAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	store := FileStore{Path: path}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() before Save error = %v, want ErrNotFound", err)
	}

	want := Credential{
		ClientID: "client", AccessToken: "xoxp-secret", RefreshToken: "xoxe-secret",
		ExpiresAt: time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC),
		TeamID:    "T1", TeamName: "Acme", UserID: "U1", Scope: "chat:write",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %04o, want 0600", fileInfo.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory mode = %04o, want 0700", directoryInfo.Mode().Perm())
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}

	replacement := want
	replacement.AccessToken = "xoxp-replacement"
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(replacement); err != nil {
		t.Fatal(err)
	}
	fileInfo, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("replacement credential mode = %04o, want 0600", fileInfo.Mode().Perm())
	}
	got, err = store.Load()
	if err != nil || got.AccessToken != replacement.AccessToken {
		t.Fatalf("Load() after replacement = %#v, %v", got, err)
	}

	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() after Delete error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("second Delete() = %v, want nil", err)
	}
}

func TestFileStoreRejectsUnsafeOrInvalidFiles(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		store := FileStore{}
		if _, err := store.Load(); err == nil {
			t.Fatal("Load() accepted an empty path")
		}
		if err := store.Save(Credential{AccessToken: "token"}); err == nil {
			t.Fatal("Save() accepted an empty path")
		}
		if err := store.Delete(); err == nil {
			t.Fatal("Delete() accepted an empty path")
		}
	})

	t.Run("empty token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := (FileStore{Path: path}).Save(Credential{}); err == nil || !strings.Contains(err.Error(), "access token") {
			t.Fatalf("Save() error = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty credential created a file: %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (FileStore{Path: path}).Load(); err == nil || !strings.Contains(err.Error(), "decode credential file") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	t.Run("oversized file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (FileStore{Path: path}).Load(); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (FileStore{Path: path}).Load(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Load() error = %v, want ErrNotFound", err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("open permissions", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, []byte(`{"access_token":"secret"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := (FileStore{Path: path}).Load(); err == nil || !strings.Contains(err.Error(), "permissions") {
				t.Fatalf("Load() error = %v", err)
			}
		})

		t.Run("symlink", func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "target.json")
			link := filepath.Join(directory, "credentials.json")
			if err := os.WriteFile(target, []byte(`{"access_token":"secret"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := (FileStore{Path: link}).Load(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestFileStoreCleansUpFailedAtomicWrite(t *testing.T) {
	directory := t.TempDir()
	targetDirectory := filepath.Join(directory, "credentials.json")
	if err := os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := FileStore{Path: targetDirectory}
	if err := store.Save(Credential{AccessToken: "token"}); err == nil {
		t.Fatal("Save() unexpectedly replaced a directory")
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(directory, ".credentials-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("Save() left temporary files behind: %v", temporaryFiles)
	}
}
