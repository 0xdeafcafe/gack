package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrNotFound = errors.New("no saved Slack login")

const (
	keychainService = "dev.0xdeafcafe.gack.slack"
	keychainAccount = "workspace"
)

type Credential struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	TeamName     string    `json:"team_name,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}

func (c Credential) NeedsRefresh(now time.Time) bool {
	return c.RefreshToken != "" && !c.ExpiresAt.IsZero() && !now.Add(5*time.Minute).Before(c.ExpiresAt)
}

type Store interface {
	Load() (Credential, error)
	Save(Credential) error
	Delete() error
}

func DefaultStore() Store {
	if runtime.GOOS == "darwin" {
		return KeychainStore{}
	}
	path, err := credentialPath()
	if err != nil {
		return failingStore{err: err}
	}
	return FileStore{Path: path}
}

type failingStore struct{ err error }

func (s failingStore) Load() (Credential, error) { return Credential{}, s.err }
func (s failingStore) Save(Credential) error     { return s.err }
func (s failingStore) Delete() error             { return s.err }

// KeychainStore uses the macOS login keychain. The Slack token is never placed
// in gack's preferences file.
type KeychainStore struct{}

func (KeychainStore) Load() (Credential, error) {
	command := exec.Command("/usr/bin/security", "find-generic-password", "-a", keychainAccount, "-s", keychainService, "-w")
	data, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return Credential{}, ErrNotFound
		}
		return Credential{}, fmt.Errorf("read macOS Keychain: %w", err)
	}
	var credential Credential
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &credential); err != nil {
		return Credential{}, fmt.Errorf("decode macOS Keychain credential: %w", err)
	}
	if credential.AccessToken == "" {
		return Credential{}, ErrNotFound
	}
	return credential, nil
}

func (KeychainStore) Save(credential Credential) error {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return errors.New("Slack access token is required")
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	command := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-a", keychainAccount, "-s", keychainService, "-w", string(data))
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("write macOS Keychain: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (KeychainStore) Delete() error {
	command := exec.Command("/usr/bin/security", "delete-generic-password", "-a", keychainAccount, "-s", keychainService)
	if output, err := command.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil
		}
		return fmt.Errorf("delete macOS Keychain credential: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// FileStore is the 0600 fallback on platforms without a native implementation.
// It is also deliberately injectable so storage behavior can be tested without
// touching a developer's real credentials.
type FileStore struct{ Path string }

func (s FileStore) Load() (Credential, error) {
	if strings.TrimSpace(s.Path) == "" {
		return Credential{}, errors.New("credential file path is required")
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Credential{}, errors.New("credential file is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Credential{}, fmt.Errorf("credential file permissions %04o are too open; want 0600", info.Mode().Perm())
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return Credential{}, fmt.Errorf("open credential file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return Credential{}, fmt.Errorf("inspect open credential file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return Credential{}, errors.New("credential file changed while it was being opened")
	}
	const maximumCredentialBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maximumCredentialBytes+1))
	if err != nil {
		return Credential{}, fmt.Errorf("read credential file: %w", err)
	}
	if len(data) > maximumCredentialBytes {
		return Credential{}, fmt.Errorf("credential file exceeds %d bytes", maximumCredentialBytes)
	}
	var credential Credential
	if err := json.Unmarshal(data, &credential); err != nil {
		return Credential{}, fmt.Errorf("decode credential file: %w", err)
	}
	if credential.AccessToken == "" {
		return Credential{}, ErrNotFound
	}
	return credential, nil
}

func (s FileStore) Save(credential Credential) error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("credential file path is required")
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return errors.New("Slack access token is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary credential file: %w", err)
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary credential file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary credential file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary credential file: %w", err)
	}
	if err := replaceFile(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	return nil
}

func (s FileStore) Delete() error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("credential file path is required")
	}
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete credential file: %w", err)
	}
	return nil
}

func credentialPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "gack", "credentials.json"), nil
}
