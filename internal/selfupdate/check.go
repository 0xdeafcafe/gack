// Package selfupdate checks gack's tagged Go module releases and safely
// replaces an installed executable when the user asks it to update.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ModulePath      = "github.com/0xdeafcafe/gack"
	CommandPath     = ModulePath + "/cmd/gack"
	DefaultEndpoint = "https://proxy.golang.org/github.com/0xdeafcafe/gack/@latest"
	defaultMaxAge   = 6 * time.Hour
	maximumResponse = 64 << 10
)

type Result struct {
	Current         string
	Latest          string
	UpdateAvailable bool
	CheckedAt       time.Time
	Cached          bool
}

type Checker struct {
	Client    *http.Client
	Endpoint  string
	CachePath string
	MaxAge    time.Duration
	Now       func() time.Time
}

type cacheRecord struct {
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
}

// DefaultChecker uses Go's public module proxy. That keeps version discovery
// tied to the same signed module/tag path used by `go install`, and does not
// require GitHub credentials or a GitHub Release object.
func DefaultChecker() Checker {
	path := ""
	if root, err := os.UserCacheDir(); err == nil {
		path = filepath.Join(root, "gack", "update.json")
	}
	return Checker{
		Client:    &http.Client{Timeout: 4 * time.Second},
		Endpoint:  DefaultEndpoint,
		CachePath: path,
		MaxAge:    defaultMaxAge,
		Now:       time.Now,
	}
}

func (checker Checker) Check(ctx context.Context, current string, force bool) (Result, error) {
	checker = checker.withDefaults()
	now := checker.Now()
	if !force {
		if cached, ok := checker.readCache(now); ok {
			return resultFor(current, cached.Version, cached.CheckedAt, true), nil
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.Endpoint, nil)
	if err != nil {
		return Result{}, fmt.Errorf("prepare update check: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "gack/"+strings.TrimPrefix(current, "v"))
	response, err := checker.Client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("check for updates: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("check for updates: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumResponse+1))
	if err != nil {
		return Result{}, fmt.Errorf("read update response: %w", err)
	}
	if len(data) > maximumResponse {
		return Result{}, errors.New("update response is unexpectedly large")
	}
	var latest struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(data, &latest); err != nil {
		return Result{}, fmt.Errorf("decode update response: %w", err)
	}
	latest.Version = strings.TrimSpace(latest.Version)
	if _, ok := parseVersion(latest.Version); !ok {
		return Result{}, fmt.Errorf("update service returned invalid version %q", latest.Version)
	}

	record := cacheRecord{Version: latest.Version, CheckedAt: now}
	checker.writeCache(record) // A cache failure must never make startup fail.
	return resultFor(current, record.Version, record.CheckedAt, false), nil
}

func (checker Checker) withDefaults() Checker {
	defaults := DefaultChecker()
	if checker.Client == nil {
		checker.Client = defaults.Client
	}
	if checker.Endpoint == "" {
		checker.Endpoint = defaults.Endpoint
	}
	if checker.MaxAge <= 0 {
		checker.MaxAge = defaults.MaxAge
	}
	if checker.Now == nil {
		checker.Now = defaults.Now
	}
	return checker
}

func (checker Checker) readCache(now time.Time) (cacheRecord, bool) {
	if checker.CachePath == "" {
		return cacheRecord{}, false
	}
	file, err := os.Open(checker.CachePath)
	if err != nil {
		return cacheRecord{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumResponse+1))
	if err != nil || len(data) > maximumResponse {
		return cacheRecord{}, false
	}
	var record cacheRecord
	if json.Unmarshal(data, &record) != nil {
		return cacheRecord{}, false
	}
	if _, ok := parseVersion(record.Version); !ok || record.CheckedAt.IsZero() {
		return cacheRecord{}, false
	}
	age := now.Sub(record.CheckedAt)
	if age < 0 || age > checker.MaxAge {
		return cacheRecord{}, false
	}
	return record, true
}

func (checker Checker) writeCache(record cacheRecord) {
	if checker.CachePath == "" {
		return
	}
	directory := filepath.Dir(checker.CachePath)
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	temporary, err := os.CreateTemp(directory, ".update-*.json")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if temporary.Chmod(0o600) != nil {
		temporary.Close()
		return
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return
	}
	if temporary.Close() != nil {
		return
	}
	_ = os.Rename(temporaryPath, checker.CachePath)
}

func resultFor(current, latest string, checkedAt time.Time, cached bool) Result {
	return Result{
		Current: current, Latest: latest, CheckedAt: checkedAt, Cached: cached,
		UpdateAvailable: IsNewer(current, latest),
	}
}

type parsedVersion struct {
	major, minor, patch uint64
	prerelease          []string
}

// IsNewer compares semantic release versions without bringing a module graph
// library into the runtime. Development builds intentionally never nag.
func IsNewer(current, latest string) bool {
	left, leftOK := parseVersion(current)
	right, rightOK := parseVersion(latest)
	return leftOK && rightOK && compareVersions(left, right) < 0
}

func parseVersion(value string) (parsedVersion, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "dev" || value == "(devel)" {
		return parsedVersion{}, false
	}
	value = strings.TrimPrefix(value, "v")
	if buildAt := strings.IndexByte(value, '+'); buildAt >= 0 {
		value = value[:buildAt]
	}
	main, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return parsedVersion{}, false
	}
	numbers := make([]uint64, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return parsedVersion{}, false
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsedVersion{}, false
		}
		numbers[index] = number
	}
	parsed := parsedVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if prerelease != "" {
		parsed.prerelease = strings.Split(prerelease, ".")
		for _, identifier := range parsed.prerelease {
			if identifier == "" {
				return parsedVersion{}, false
			}
		}
	}
	return parsed, true
}

func compareVersions(left, right parsedVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) > 0 {
		return 1
	}
	if len(right.prerelease) == 0 && len(left.prerelease) > 0 {
		return -1
	}
	for index := 0; index < min(len(left.prerelease), len(right.prerelease)); index++ {
		leftID, rightID := left.prerelease[index], right.prerelease[index]
		leftNumber, leftErr := strconv.ParseUint(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightID, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case leftID < rightID:
			return -1
		case leftID > rightID:
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}
