package main

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/0xdeafcafe/gack/internal/slackapp"
)

func TestExplicitBuildVersionWins(t *testing.T) {
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("buildVersion() = %q", got)
	}
}

func TestRunManifestPrintsCanonicalManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runManifest(nil, &stdout, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != slackapp.Manifest() {
		t.Fatal("manifest command did not print the canonical manifest")
	}
	if stderr.Len() != 0 {
		t.Fatalf("manifest command wrote unexpected stderr: %q", stderr.String())
	}
}

func TestRunManifestCanOpenPrefilledCreator(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var opened string
	err := runManifest([]string{"--open"}, &stdout, &stderr, func(target string) error {
		opened = target
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if target.Query().Get("manifest_yaml") != stdout.String() {
		t.Fatal("browser URL and printed manifest differ")
	}
	if !strings.Contains(stderr.String(), "copy its Client ID") {
		t.Fatalf("setup instructions were not shown: %q", stderr.String())
	}
}

func TestRunManifestRejectsUnexpectedArguments(t *testing.T) {
	err := runManifest([]string{"somewhere.yaml"}, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}
