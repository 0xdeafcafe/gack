package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Preferences struct {
	ChannelOrder []string `json:"channel_order,omitempty"`
}

func Path() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "gack", "config.json"), nil
}

func Load() (Preferences, error) {
	path, err := Path()
	if err != nil {
		return Preferences{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, err
	}
	var preferences Preferences
	if err := json.Unmarshal(data, &preferences); err != nil {
		return Preferences{}, err
	}
	return preferences, nil
}

func Save(preferences Preferences) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(preferences, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func ApplyOrder[T any](items []T, ids func(T) string, order []string) []T {
	if len(order) == 0 {
		return items
	}
	positions := make(map[string]int, len(order))
	for i, id := range order {
		positions[id] = i
	}
	result := append([]T(nil), items...)
	for i := 1; i < len(result); i++ {
		for j := i; j > 0; j-- {
			left, leftOK := positions[ids(result[j-1])]
			right, rightOK := positions[ids(result[j])]
			if !leftOK {
				left = len(order) + j - 1
			}
			if !rightOK {
				right = len(order) + j
			}
			if left <= right {
				break
			}
			result[j-1], result[j] = result[j], result[j-1]
		}
	}
	return result
}
