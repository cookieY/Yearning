// announcements.go announcement domain (S005): append-only revisions (sanitized HTML) + singleton
// publication (optimistic lock). Sanitize uses an allowlist policy (no scripts, event attributes, or external scripts).
package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SanitizerPolicyVersion is the current allowlist policy version (must increment when the policy is updated).
const SanitizerPolicyVersion = "allowlist-1"

// ErrAnnouncementValidation indicates an announcement validation error.
var ErrAnnouncementValidation = fmt.Errorf("dashboard: announcement validation failed")

// AnnouncementRevisionModel maps to announcement_revisions.
type AnnouncementRevisionModel struct {
	ID                string    `gorm:"column:id;primaryKey"`
	RevisionNumber    int64     `gorm:"column:revision_number"`
	Title             string    `gorm:"column:title"`
	MarkdownSource    string    `gorm:"column:markdown_source"`
	SanitizedHTML     string    `gorm:"column:sanitized_html"`
	ContentSHA256     string    `gorm:"column:content_sha256"`
	SanitizerPolicy   string    `gorm:"column:sanitizer_policy_version"`
	CreatedByUserID   string    `gorm:"column:created_by_user_id_snapshot"`
	CreatedByUsername string    `gorm:"column:created_by_username_snapshot"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (AnnouncementRevisionModel) TableName() string { return "announcement_revisions" }

// AnnouncementPublicationModel maps to announcement_publication (singleton row).
type AnnouncementPublicationModel struct {
	Singleton    bool      `gorm:"column:singleton;primaryKey"`
	RevisionID   string    `gorm:"column:announcement_revision_id"`
	PublishedBy  string    `gorm:"column:published_by_user_id_snapshot"`
	PublishedByN string    `gorm:"column:published_by_username_snapshot"`
	PublishedAt  time.Time `gorm:"column:published_at"`
	Version      int64     `gorm:"column:version"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (AnnouncementPublicationModel) TableName() string { return "announcement_publication" }

// allowedMarkdownTokens allowlist (headings, emphasis, lists, links, code, blockquotes;
// everything else is escaped). v1 is a deterministic line-oriented converter (introducing no
// third-party HTML parser dependency) whose output HTML never contains <script>, <iframe>, on* attributes, or javascript: links.
func sanitizeMarkdown(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		// Recognize structural prefixes first (#/-/> contain no special characters), then convert
		// inline formatting on raw text and escape immediately (BUG-012 fix).
		switch {
		case strings.HasPrefix(line, "### "):
			b.WriteString("<h3>" + inline(strings.TrimPrefix(line, "### ")) + "</h3>")
		case strings.HasPrefix(line, "## "):
			b.WriteString("<h2>" + inline(strings.TrimPrefix(line, "## ")) + "</h2>")
		case strings.HasPrefix(line, "# "):
			b.WriteString("<h1>" + inline(strings.TrimPrefix(line, "# ")) + "</h1>")
		case strings.HasPrefix(line, "- "):
			b.WriteString("<ul><li>" + inline(strings.TrimPrefix(line, "- ")) + "</li></ul>")
		case strings.HasPrefix(line, "> "):
			b.WriteString("<blockquote>" + inline(strings.TrimPrefix(line, "> ")) + "</blockquote>")
		default:
			b.WriteString("<p>" + inline(line) + "</p>")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// inline converts inline formatting: **bold** / `code` / [text](http or https links only).
// Inline conversion operates on raw text (BUG-012 fix: previously escapeHTML ran first and turned
// backticks into &#96;, destroying inline code delimiters so `code` never rendered). Both inner
// content of each construct and residual text are escaped on the fly, producing zero raw HTML.
func inline(s string) string {
	// Cursor-based inline conversion: escapes only text between constructs without re-escaping
	// generated tag markup (BUG-012 fix; the old implementation either escaped before matching or
	// re-escaped the entire string at the end, swallowing tags or backtick delimiters). Unclosed markers are escaped as plain text.
	var b strings.Builder
	for {
		iBold := strings.Index(s, "**")
		iCode := strings.Index(s, "`")
		if iBold < 0 && iCode < 0 {
			b.WriteString(escapeHTML(s))
			return b.String()
		}
		if iCode < 0 || (iBold >= 0 && iBold < iCode) {
			j := strings.Index(s[iBold+2:], "**")
			if j < 0 {
				b.WriteString(escapeHTML(s[:iBold+2]))
				s = s[iBold+2:]
				continue
			}
			b.WriteString(escapeHTML(s[:iBold]))
			b.WriteString("<strong>" + escapeHTML(s[iBold+2:iBold+2+j]) + "</strong>")
			s = s[iBold+2+j+2:]
			continue
		}
		j := strings.Index(s[iCode+1:], "`")
		if j < 0 {
			b.WriteString(escapeHTML(s[:iCode+1]))
			s = s[iCode+1:]
			continue
		}
		b.WriteString(escapeHTML(s[:iCode]))
		b.WriteString("<code>" + escapeHTML(s[iCode+1:iCode+1+j]) + "</code>")
		s = s[iCode+1+j+1:]
	}
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&#34;", "'", "&#39;", "`", "&#96;")
	return r.Replace(s)
}

// CreateRevisionInput holds parameters for creating an announcement revision.
type CreateRevisionInput struct {
	Title         string
	Markdown      string
	ActorID       string
	ActorUsername string
}

// AnnouncementRevisionView conforms to OpenAPI AnnouncementRevision.
type AnnouncementRevisionView struct {
	ID             string `json:"id"`
	RevisionNumber int64  `json:"revision_number"`
	Title          string `json:"title"`
	SanitizedHTML  string `json:"sanitized_html"`
	ContentSHA256  string `json:"content_sha256"`
	PolicyVersion  string `json:"sanitizer_policy_version"`
	CreatedBy      string `json:"created_by_username"`
	CreatedAt      string `json:"created_at"`
}

// CreateRevision creates an announcement revision (sanitize -> hash -> append; never mutates existing revisions).
func (s *Service) CreateRevision(ctx context.Context, in CreateRevisionInput) (*AnnouncementRevisionView, error) {
	if in.Title == "" || len(in.Title) > 200 {
		return nil, ErrAnnouncementValidation
	}
	if in.Markdown == "" || len(in.Markdown) > 20000 {
		return nil, ErrAnnouncementValidation
	}
	html := sanitizeMarkdown(in.Markdown)
	if html == "" {
		return nil, ErrAnnouncementValidation
	}
	sum := sha256.Sum256([]byte(in.Markdown))
	now := s.Now()
	// Atomically allocate revision_number in a single statement (insert...select max+1):
	// concurrent creations are arbitrated by 23505 unique conflict with no read-write race window.
	row := &AnnouncementRevisionModel{
		ID:    uuid.NewString(),
		Title: in.Title, MarkdownSource: in.Markdown, SanitizedHTML: html,
		ContentSHA256: hex.EncodeToString(sum[:]), SanitizerPolicy: SanitizerPolicyVersion,
		CreatedByUserID: in.ActorID, CreatedByUsername: in.ActorUsername, CreatedAt: now,
	}
	if err := s.DB.WithContext(ctx).Exec(`
		insert into announcement_revisions (id, revision_number, title, markdown_source,
		    sanitized_html, content_sha256, sanitizer_policy_version,
		    created_by_user_id_snapshot, created_by_username_snapshot, created_at)
		select ?, coalesce(max(revision_number), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?
		from announcement_revisions`,
		row.ID, row.Title, row.MarkdownSource, row.SanitizedHTML, row.ContentSHA256,
		row.SanitizerPolicy, row.CreatedByUserID, row.CreatedByUsername, row.CreatedAt).Error; err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Where("id = ?", row.ID).First(row).Error; err != nil {
		return nil, err
	}
	return &AnnouncementRevisionView{
		ID: row.ID, RevisionNumber: row.RevisionNumber, Title: row.Title,
		SanitizedHTML: row.SanitizedHTML, ContentSHA256: row.ContentSHA256,
		PolicyVersion: row.SanitizerPolicy, CreatedBy: row.CreatedByUsername,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// ListRevisions lists revision history (bounded in descending order).
func (s *Service) ListRevisions(ctx context.Context, limit int) ([]AnnouncementRevisionView, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows []AnnouncementRevisionModel
	if err := s.DB.WithContext(ctx).Order("revision_number desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AnnouncementRevisionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, AnnouncementRevisionView{
			ID: r.ID, RevisionNumber: r.RevisionNumber, Title: r.Title,
			SanitizedHTML: r.SanitizedHTML, ContentSHA256: r.ContentSHA256,
			PolicyVersion: r.SanitizerPolicy, CreatedBy: r.CreatedByUsername,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// Publish publishes a revision (singleton row optimistic-lock upsert; publication records cannot be forged).
// Returns (revision view, new publication pointer version): insert branch = 1, update branch = expectVersion + 1
// — matching the persisted DB value (R2-F12: never hand-computed in the handler).
func (s *Service) Publish(ctx context.Context, revisionID string, actorID, actorUsername string, expectVersion int64) (*AnnouncementRevisionView, int64, error) {
	var rev AnnouncementRevisionModel
	if err := s.DB.WithContext(ctx).Where("id = ?", revisionID).First(&rev).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	now := s.Now()
	res := s.DB.WithContext(ctx).Exec(`
		insert into announcement_publication (singleton, announcement_revision_id,
		    published_by_user_id_snapshot, published_by_username_snapshot, published_at, version, updated_at)
		values (true, ?, ?, ?, ?, 1, ?)
		on conflict (singleton) do update set announcement_revision_id = excluded.announcement_revision_id,
		    published_by_user_id_snapshot = excluded.published_by_user_id_snapshot,
		    published_by_username_snapshot = excluded.published_by_username_snapshot,
		    published_at = excluded.published_at, version = announcement_publication.version + 1,
		    updated_at = excluded.updated_at
		where announcement_publication.version = ?`,
		revisionID, actorID, actorUsername, now, now, expectVersion)
	if res.Error != nil {
		return nil, 0, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, 0, ErrConflict
	}
	// Persisted pointer version truth (R2-F12): insert = 1, update = expect + 1, but
	// RowsAffected cannot distinguish the two branches — re-read the singleton row to prevent drift.
	var pubVersion int64
	if err := s.DB.WithContext(ctx).Raw(
		`select version from announcement_publication where singleton = true`).Row().Scan(&pubVersion); err != nil {
		return nil, 0, err
	}
	return &AnnouncementRevisionView{
		ID: rev.ID, RevisionNumber: rev.RevisionNumber, Title: rev.Title,
		SanitizedHTML: rev.SanitizedHTML, ContentSHA256: rev.ContentSHA256,
		PolicyVersion: rev.SanitizerPolicy, CreatedBy: rev.CreatedByUsername,
		CreatedAt: rev.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, pubVersion, nil
}

// Current returns the currently published announcement (anonymously readable; unpublished -> nil, nil).
// View conforms to OpenAPI AnnouncementPublication (additionalProperties:false, published_by_username required).
func (s *Service) Current(ctx context.Context) (*struct {
	Revision    AnnouncementRevisionView `json:"revision"`
	PublishedBy string                   `json:"published_by_username"`
	Published   string                   `json:"published_at"`
	Version     int64                    `json:"version"`
}, error) {
	var pub AnnouncementPublicationModel
	if err := s.DB.WithContext(ctx).Where("singleton = true").First(&pub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var rev AnnouncementRevisionModel
	if err := s.DB.WithContext(ctx).Where("id = ?", pub.RevisionID).First(&rev).Error; err != nil {
		return nil, err
	}
	return &struct {
		Revision    AnnouncementRevisionView `json:"revision"`
		PublishedBy string                   `json:"published_by_username"`
		Published   string                   `json:"published_at"`
		Version     int64                    `json:"version"`
	}{
		Revision: AnnouncementRevisionView{
			ID: rev.ID, RevisionNumber: rev.RevisionNumber, Title: rev.Title,
			SanitizedHTML: rev.SanitizedHTML, ContentSHA256: rev.ContentSHA256,
			PolicyVersion: rev.SanitizerPolicy, CreatedBy: rev.CreatedByUsername,
			CreatedAt: rev.CreatedAt.UTC().Format(time.RFC3339Nano),
		},
		PublishedBy: pub.PublishedByN,
		Published:   pub.PublishedAt.UTC().Format(time.RFC3339Nano),
		Version:     pub.Version,
	}, nil
}

var _ = json.Marshal
