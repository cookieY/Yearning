//go:build release_manifests

package k8s

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReleaseManifestsPinned SEC-0016/SEC-0019: release-rendered artifacts must replace the sole
// allowed placeholder with a real digest. Runs only when RELEASE_MANIFEST_DIR points to the rendered
// directory (make release-render-manifests / release workflow).
func TestReleaseManifestsPinned(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("RELEASE_MANIFEST_DIR"))
	if dir == "" {
		t.Fatal("RELEASE_MANIFEST_DIR must be set when running -tags release_manifests")
	}
	docs := loadManifestsFrom(t, dir)
	if len(docs) == 0 {
		t.Fatalf("no rendered manifests under %s", dir)
	}
	pinned := regexp.MustCompile(`^ghcr\.io/cookiey/yearning@sha256:[0-9a-f]{64}$`)
	images := 0
	for _, d := range docs {
		for _, ref := range yamlImages(d.Body) {
			images++
			if strings.Contains(ref, "REPLACED_BY_RELEASE") {
				t.Errorf("%s %s: release placeholder survived rendering: %s", d.Kind, d.Name, ref)
			}
			if !pinned.MatchString(ref) {
				t.Errorf("%s %s: image not digest-pinned in rendered manifest: %s", d.Kind, d.Name, ref)
			}
		}
	}
	if images < 3 {
		t.Fatalf("expected >=3 rendered image references, got %d", images)
	}
}
