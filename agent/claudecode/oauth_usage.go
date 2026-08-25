package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Quota windows are read from Claude's OAuth usage endpoint using the
// credential Claude Code already stores locally.
//
// The alternative in this package, runClaudeUsageProbe, drives the interactive
// TUI and scrapes its screen. That is slow enough to blow a sub-second caller
// deadline and breaks whenever the TUI is restyled, so the endpoint is tried
// first and the probe is kept only as a fallback.
const (
	oauthUsageURL      = "https://api.anthropic.com/api/oauth/usage"
	oauthUsageTimeout  = 5 * time.Second
	oauthUsageCacheTTL = 60 * time.Second

	fiveHourWindowSeconds = 18000
	sevenDayWindowSeconds = 604800
)

type oauthUsageCache struct {
	mu        sync.Mutex
	report    *core.UsageReport
	fetchedAt time.Time
	lastErr   error
}

var usageCache oauthUsageCache

type oauthUsagePeriod struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type oauthUsageResponse struct {
	FiveHour *oauthUsagePeriod `json:"five_hour"`
	SevenDay *oauthUsagePeriod `json:"seven_day"`
}

// claudeCredentialPath returns the location of Claude Code's stored OAuth
// credential.
func claudeCredentialPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// readClaudeAccessToken loads the OAuth access token Claude Code stores for the
// signed-in account. The token is never logged.
func readClaudeAccessToken() (string, error) {
	path := claudeCredentialPath()
	if path == "" {
		return "", fmt.Errorf("claudecode: cannot resolve home directory")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("claudecode: reading credentials: %w", err)
	}
	var parsed struct {
		ClaudeAIOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("claudecode: parsing credentials: %w", err)
	}
	token := parsed.ClaudeAIOauth.AccessToken
	if token == "" {
		return "", fmt.Errorf("claudecode: no OAuth access token stored")
	}
	// expiresAt is in milliseconds. A stale token yields a 401 that is far less
	// obvious than saying so here.
	if exp := parsed.ClaudeAIOauth.ExpiresAt; exp > 0 && time.Now().After(time.UnixMilli(exp)) {
		return "", fmt.Errorf("claudecode: stored OAuth token expired; re-authenticate in Claude Code")
	}
	return token, nil
}

// toUsageWindow converts one API period into a core window. It returns false
// when the period is absent or carries no utilization, so an unreported quota
// is omitted rather than rendered as zero.
func toUsageWindow(name string, windowSeconds int, period *oauthUsagePeriod, now time.Time) (core.UsageWindow, bool) {
	if period == nil || period.Utilization == nil {
		return core.UsageWindow{}, false
	}
	window := core.UsageWindow{
		Name:          name,
		UsedPercent:   int(*period.Utilization + 0.5),
		WindowSeconds: windowSeconds,
	}
	if period.ResetsAt != nil && *period.ResetsAt != "" {
		if resetAt, err := time.Parse(time.RFC3339Nano, *period.ResetsAt); err == nil {
			window.ResetAtUnix = resetAt.Unix()
			if after := int(resetAt.Sub(now).Round(time.Second).Seconds()); after > 0 {
				window.ResetAfterSeconds = after
			}
		}
	}
	return window, true
}

// fetchOAuthUsage retrieves the current quota windows, memoised for
// oauthUsageCacheTTL so a per-message caller cannot hammer the endpoint.
func fetchOAuthUsage(ctx context.Context) (*core.UsageReport, error) {
	usageCache.mu.Lock()
	if usageCache.report != nil && time.Since(usageCache.fetchedAt) < oauthUsageCacheTTL {
		cached := usageCache.report
		usageCache.mu.Unlock()
		return cached, nil
	}
	// A recent failure is cached too, so a broken credential does not mean a
	// blocking request on every single message.
	if usageCache.report == nil && usageCache.lastErr != nil && time.Since(usageCache.fetchedAt) < oauthUsageCacheTTL {
		err := usageCache.lastErr
		usageCache.mu.Unlock()
		return nil, err
	}
	usageCache.mu.Unlock()

	report, err := requestOAuthUsage(ctx)

	usageCache.mu.Lock()
	usageCache.fetchedAt = time.Now()
	usageCache.lastErr = err
	if err == nil {
		usageCache.report = report
	}
	usageCache.mu.Unlock()
	return report, err
}

func requestOAuthUsage(ctx context.Context) (*core.UsageReport, error) {
	token, err := readClaudeAccessToken()
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, oauthUsageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, oauthUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claudecode: usage request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claudecode: usage API returned %d", resp.StatusCode)
	}

	var parsed oauthUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("claudecode: decoding usage response: %w", err)
	}

	now := time.Now()
	var windows []core.UsageWindow
	if w, ok := toUsageWindow("5h", fiveHourWindowSeconds, parsed.FiveHour, now); ok {
		windows = append(windows, w)
	}
	if w, ok := toUsageWindow("7d", sevenDayWindowSeconds, parsed.SevenDay, now); ok {
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("claudecode: usage API reported no quota windows")
	}

	return &core.UsageReport{
		Provider: "claude",
		Buckets:  []core.UsageBucket{{Name: "session", Allowed: true, Windows: windows}},
	}, nil
}
