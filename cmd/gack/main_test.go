package main

import "testing"

func TestExplicitBuildVersionWins(t *testing.T) {
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("buildVersion() = %q", got)
	}
}
