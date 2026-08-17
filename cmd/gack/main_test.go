package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/0xdeafcafe/gack/internal/selfupdate"
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

func TestRunUpdateCheckReportsAvailableRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"Version":"v1.3.0"}`)
	}))
	defer server.Close()
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })

	var stdout, stderr bytes.Buffer
	checker := selfupdate.Checker{
		Client: server.Client(), Endpoint: server.URL,
		Now: time.Now,
	}
	err := runUpdate([]string{"--check"}, &stdout, &stderr, checker, selfupdate.Installer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "v1.3.0 is available") || !strings.Contains(stdout.String(), "gack update") {
		t.Fatalf("unexpected check output: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected check stderr: %q", stderr.String())
	}
}
