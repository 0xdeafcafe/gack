package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestIsNewerUsesSemanticVersionOrder(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"v0.3.0", "v0.4.0", true},
		{"v0.10.0", "v0.9.9", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0+dirty", "v1.0.0", false},
		{"dev", "v99.0.0", false},
		{"nonsense", "v1.0.0", false},
	}
	for _, test := range tests {
		if got := IsNewer(test.current, test.latest); got != test.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", test.current, test.latest, got, test.want)
		}
	}
}

func TestCheckerFetchesAndCachesLatestVersion(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("User-Agent") != "gack/0.3.0" {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		fmt.Fprint(writer, `[{"name":"v0.4.0"},{"name":"not-a-release"},{"name":"v0.3.0"}]`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	checker := Checker{
		Client: server.Client(), Endpoint: server.URL,
		CachePath: filepath.Join(t.TempDir(), "cache", "update.json"),
		MaxAge:    time.Hour, Now: func() time.Time { return now },
	}
	first, err := checker.Check(context.Background(), "v0.3.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.UpdateAvailable || first.Latest != "v0.4.0" || first.Cached {
		t.Fatalf("first result = %#v", first)
	}

	server.Close()
	second, err := checker.Check(context.Background(), "v0.3.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdateAvailable || !second.Cached || calls != 1 {
		t.Fatalf("cached result = %#v, calls = %d", second, calls)
	}
}

func TestCheckerRejectsInvalidVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `[{"name":"definitely-not-a-release"}]`)
	}))
	defer server.Close()
	checker := Checker{Client: server.Client(), Endpoint: server.URL, Now: time.Now}
	if _, err := checker.Check(context.Background(), "v0.3.0", true); err == nil {
		t.Fatal("invalid update version was accepted")
	}
}
