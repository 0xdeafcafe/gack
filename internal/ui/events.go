package ui

import "github.com/0xdeafcafe/gack/internal/gack"

// These values are the complete event vocabulary produced by UI reactors.
// They contain data only: no callbacks, channels, or mutable shared state.

type bootstrapResult struct {
	snapshot gack.Snapshot
	err      error
}

type messagesResult struct {
	channel    string
	messages   []gack.Message
	nextCursor string
	more       bool
	err        error
}

type threadResult struct {
	channel    string
	thread     string
	replies    []gack.Message
	nextCursor string
	more       bool
	err        error
}

type postResult struct {
	channel string
	thread  string
	message gack.Message
	err     error
}

type editResult struct {
	channel string
	ts      string
	message gack.Message
	err     error
}

type searchResult struct {
	results []gack.SearchResult
	err     error
}

type activityResult struct {
	items      []gack.ActivityItem
	background bool
	err        error
}

type activityPollTick struct{}

type notificationResult struct{ err error }

type interactionResult struct {
	result gack.InteractionResult
	err    error
}

type reactionResult struct {
	channel string
	thread  string
	ts      string
	emoji   string
	remove  bool
	err     error
}

type sidebarSaved struct {
	revision uint64
	notice   string
	err      error
}

type copiedResult struct{ err error }

type versionResult struct {
	latest string
	err    error
}
