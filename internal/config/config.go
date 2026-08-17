package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// SidebarSort controls how conversations are presented in the sidebar. The
// manual order is kept independently so switching to a computed sort and back
// never destroys a user's arrangement.
type SidebarSort string

const (
	SidebarSortManual       SidebarSort = "manual"
	SidebarSortAlphabetical SidebarSort = "alphabetical"
	SidebarSortAttention    SidebarSort = "attention"
)

func (sort SidebarSort) Normalize() SidebarSort {
	switch sort {
	case SidebarSortManual, SidebarSortAlphabetical, SidebarSortAttention:
		return sort
	default:
		return SidebarSortManual
	}
}

func (sort SidebarSort) Next() SidebarSort {
	switch sort.Normalize() {
	case SidebarSortManual:
		return SidebarSortAlphabetical
	case SidebarSortAlphabetical:
		return SidebarSortAttention
	default:
		return SidebarSortManual
	}
}

func (sort SidebarSort) Label() string {
	switch sort.Normalize() {
	case SidebarSortAlphabetical:
		return "Alphabetical"
	case SidebarSortAttention:
		return "Attention"
	default:
		return "Manual"
	}
}

type Preferences struct {
	ChannelOrder  []string    `json:"channel_order,omitempty"`
	SidebarSort   SidebarSort `json:"sidebar_sort,omitempty"`
	SlackClientID string      `json:"slack_client_id,omitempty"`
}

// SidebarPreferences is the small subset of Preferences owned by the TUI.
// Keeping this value typed prevents the UI from having to know how the rest of
// the application configuration is stored.
type SidebarPreferences struct {
	ChannelOrder []string
	Sort         SidebarSort
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
	preferences.SidebarSort = preferences.SidebarSort.Normalize()
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
	preferences.SidebarSort = preferences.SidebarSort.Normalize()
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
		return append([]T(nil), items...)
	}
	positions := make(map[string]int, len(order))
	for i, id := range order {
		if _, exists := positions[id]; !exists {
			positions[id] = i
		}
	}
	result := append([]T(nil), items...)
	slices.SortStableFunc(result, func(left, right T) int {
		leftPosition, leftKnown := positions[ids(left)]
		rightPosition, rightKnown := positions[ids(right)]
		switch {
		case leftKnown && rightKnown:
			return cmp.Compare(leftPosition, rightPosition)
		case leftKnown:
			return -1
		case rightKnown:
			return 1
		default:
			return 0
		}
	})
	return result
}
