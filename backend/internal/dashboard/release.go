// release.go upstream release snapshot (RCP-20261020-RELEASE-FEED-BACKEND).
// The home page version banner no longer fetches api.github.com directly from the browser —
// unauthenticated quota is shared per IP (60/h), causing the banner and layout to disappear when exhausted.
// Instead, the backend fetches upstream releases/latest at process startup and once daily, persisting the
// snapshot in the singleton upstream_release table; readers query only the local snapshot (data:null when
// no snapshot exists yet). Fetch failures preserve the previous snapshot and log a warning, retrying with
// short backoff until the first snapshot succeeds before returning to the daily cadence.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

const (
	// DefaultReleaseFeedURL is the default upstream source; overridable via config release_feed.upstream_url
	// (YEARNING_RELEASE_FEED_UPSTREAM_URL) for tests or internal mirrors.
	DefaultReleaseFeedURL = "https://api.github.com/repos/cookieY/Yearning/releases/latest"
	// ReleaseSuccessInterval per owner ruling: once per day.
	ReleaseSuccessInterval = 24 * time.Hour
	// releaseRetryInterval short backoff on failure — prevents the placeholder banner from remaining
	// for a full day if the initial fetch is rate-limited, while preserving the daily cadence after success.
	releaseRetryInterval = 5 * time.Minute
	// releaseBodyLimit caps the truncated release body length (GitHub release bodies can be arbitrarily long).
	releaseBodyLimit = 32 << 10
)

// releaseHTTPClient dedicated HTTP client with timeout; tests may swap its Transport.
var releaseHTTPClient = &http.Client{Timeout: 15 * time.Second}

// UpstreamReleaseModel maps to upstream_release (instance-wide singleton snapshot row).
type UpstreamReleaseModel struct {
	Singleton      bool       `gorm:"column:singleton;primaryKey"`
	TagName        string     `gorm:"column:tag_name"`
	ReleaseName    string     `gorm:"column:release_name"`
	HTMLURL        string     `gorm:"column:html_url"`
	Body           string     `gorm:"column:body"`
	PublishedAt    *time.Time `gorm:"column:published_at"`
	FetchedAt      time.Time  `gorm:"column:fetched_at"`
	CurrentVersion *string    `gorm:"column:current_version"`
}

func (UpstreamReleaseModel) TableName() string { return "upstream_release" }

// ReleaseSnapshotView conforms to OpenAPI ReleaseSnapshot; timestamps are always RFC3339 UTC,
// published_at is null when unknown (GitHub drafts have no publish timestamp), and current_version is null
// when no installed baseline exists yet (fresh install before the initial fetch succeeds; frontend hides 'Update now').
type ReleaseSnapshotView struct {
	TagName        string  `json:"tag_name"`
	Name           string  `json:"name"`
	HTMLURL        string  `json:"html_url"`
	Body           string  `json:"body"`
	PublishedAt    *string `json:"published_at"`
	FetchedAt      string  `json:"fetched_at"`
	CurrentVersion *string `json:"current_version"`
}

// RefreshReleaseSnapshot fetches upstream once and upserts the snapshot; any failure returns an error
// without touching existing DB snapshots. When seedCurrent=true (first successful fetch after serve startup),
// the latest upstream tag at that moment is also written to current_version — installing or upgrading restarts
// the process, whereas runtime refreshes (seedCurrent=false) update only snapshot columns and leave current_version untouched.
func (s *Service) RefreshReleaseSnapshot(ctx context.Context, source string, seedCurrent bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return fmt.Errorf("release feed: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "YearningX-Server")
	client := s.ReleaseHTTPClient
	if client == nil {
		client = releaseHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("release feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release feed: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, releaseBodyLimit+1))
	if err != nil {
		return fmt.Errorf("release feed: read body: %w", err)
	}
	if len(raw) > releaseBodyLimit {
		raw = raw[:releaseBodyLimit]
	}
	var payload struct {
		TagName     string  `json:"tag_name"`
		Name        *string `json:"name"`
		HTMLURL     string  `json:"html_url"`
		Body        *string `json:"body"`
		PublishedAt *string `json:"published_at"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("release feed: decode: %w", err)
	}
	if payload.TagName == "" || payload.HTMLURL == "" {
		return fmt.Errorf("release feed: payload missing tag_name/html_url")
	}
	name, body := "", ""
	if payload.Name != nil {
		name = *payload.Name
	}
	if payload.Body != nil {
		body = *payload.Body
	}
	var publishedAt any
	if payload.PublishedAt != nil && *payload.PublishedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *payload.PublishedAt)
		if err != nil {
			return fmt.Errorf("release feed: published_at %q: %w", *payload.PublishedAt, err)
		}
		publishedAt = parsed.UTC()
	}
	// current_version: equals tag_name during seed writes, or NULL during runtime refreshes (the insert branch
	// is reached by a runtime refresh only when the table is completely empty, where no prior baseline exists to preserve).
	var currentVersion any
	if seedCurrent {
		currentVersion = payload.TagName
	}
	query := `
		insert into upstream_release (singleton, tag_name, release_name, html_url, body, published_at, fetched_at, current_version)
		values (true, ?, ?, ?, ?, ?, ?, ?)
		on conflict (singleton) do update set
			tag_name = excluded.tag_name,
			release_name = excluded.release_name,
			html_url = excluded.html_url,
			body = excluded.body,
			published_at = excluded.published_at,
			fetched_at = excluded.fetched_at`
	args := []any{payload.TagName, name, payload.HTMLURL, body, publishedAt, s.Now().UTC(), currentVersion}
	if seedCurrent {
		query += `, current_version = excluded.current_version`
	}
	return s.DB.WithContext(ctx).Exec(query, args...).Error
}

// RunReleaseRefreshLoop performs an immediate fetch on the first iteration (per owner ruling: fetch once at startup),
// refreshing daily after success or retrying with short backoff on failure. The first successful fetch after startup
// also seeds current_version (install/upgrade baseline = latest upstream release at restart); subsequent runtime
// refreshes update only the snapshot. Failures never terminate the process and preserve the last successful DB snapshot.
// Launched as a goroutine by the caller without blocking the listener.
func (s *Service) RunReleaseRefreshLoop(ctx context.Context, source string, logger *zap.Logger) {
	interval := ReleaseSuccessInterval
	seeded := false
	for {
		if err := s.RefreshReleaseSnapshot(ctx, source, !seeded); err != nil {
			interval = releaseRetryInterval
			logger.Warn("release feed refresh failed; keeping previous snapshot",
				zap.String("source", source), zap.Error(err))
		} else {
			seeded = true
			interval = ReleaseSuccessInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// LatestRelease reads the current snapshot; returns (nil, nil) when no snapshot exists yet -> reader emits data:null.
func (s *Service) LatestRelease(ctx context.Context) (*ReleaseSnapshotView, error) {
	var row struct {
		TagName        string     `gorm:"column:tag_name"`
		ReleaseName    string     `gorm:"column:release_name"`
		HTMLURL        string     `gorm:"column:html_url"`
		Body           string     `gorm:"column:body"`
		PublishedAt    *time.Time `gorm:"column:published_at"`
		FetchedAt      time.Time  `gorm:"column:fetched_at"`
		CurrentVersion *string    `gorm:"column:current_version"`
	}
	err := s.DB.WithContext(ctx).Raw(`
		select tag_name, release_name, html_url, body, published_at, fetched_at, current_version
		from upstream_release where singleton = true`).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.TagName == "" {
		return nil, nil
	}
	view := &ReleaseSnapshotView{
		TagName:        row.TagName,
		Name:           row.ReleaseName,
		HTMLURL:        row.HTMLURL,
		Body:           row.Body,
		CurrentVersion: row.CurrentVersion,
	}
	if row.PublishedAt != nil {
		published := row.PublishedAt.UTC().Format(time.RFC3339)
		view.PublishedAt = &published
	}
	view.FetchedAt = row.FetchedAt.UTC().Format(time.RFC3339)
	return view, nil
}
