// spa.go implements the release-build SPA serving handler (FE-F13).
// Implemented in pure net/http without gin dependencies: once `-tags assets` release builds embed
// the Frontend static artifacts via this package, the host process mounts Handler as a NoRoute fallback.
// F13's allowed_paths exclude backend/internal/httpapi (router wiring belongs to a BE track package boundary),
// so this package delivers a self-contained Provider+Handler; router mounting is wired via a one-line
// NoRoute hook in the subsequent BE package (see notes/boundary-decision.md).
// The release binary itself already embeds the hash-bound locked artifacts asserted by release_frontend_scan --
// router wiring does not alter the gate invariant that the image contains only locked React artifacts.
package assets

import (
	"io/fs"
	"net/http"
	"strings"
)

// Handler returns the SPA fallback http.Handler:
//   - unmatched paths under apiPrefix are delegated to notFound (API contract 404 semantics take precedence
//     so frontend routing never swallows API 404 observability);
//   - matching static files are served directly (with immutable cache headers for Vite content-hashed assets);
//   - unmatched non-API paths fall back to index.html (handled by client-side routing).
//
// When notFound is nil, unmatched API paths fall back to http.NotFound. Returns nil when prov is disabled.
func Handler(prov Provider, notFound http.HandlerFunc) http.Handler {
	if !prov.Enabled() {
		return nil
	}
	if notFound == nil {
		notFound = http.NotFound
	}
	fileServer := http.FileServerFS(prov.FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Treat bare "/api" (without trailing slash) as an API prefix as well so SPA fallback never swallows /api 404s.
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			notFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if _, err := fs.Stat(prov.FS(), name); err != nil {
			// Static file does not exist: treat as a client-side SPA route and fall back to the entry document.
			// Disable caching on index.html: after a new deployment, a cached entry document referencing old
			// content-hashed assets that no longer exist would cause a blank screen, so index.html must revalidate on every request.
			r.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(name, "assets/") {
			// Vite outputs under assets/ are content-hashed and immutable.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}
