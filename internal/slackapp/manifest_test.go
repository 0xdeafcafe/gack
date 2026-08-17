package slackapp

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManifestMatchesCheckedInExample(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate manifest test")
	}
	example, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "slack-manifest.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Manifest(); got != string(example) {
		t.Fatalf("generated manifest differs from slack-manifest.example.yaml\n--- generated ---\n%s\n--- checked in ---\n%s", got, example)
	}
}

func TestCreationURLContainsManifest(t *testing.T) {
	target, err := url.Parse(CreationURL())
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "https" || target.Host != "api.slack.com" || target.Path != "/apps" {
		t.Fatalf("unexpected creation URL: %s", target)
	}
	if target.Query().Get("new_app") != "1" || target.Query().Get("manifest_yaml") != Manifest() {
		t.Fatalf("creation URL does not contain the generated manifest: %s", target)
	}
}
