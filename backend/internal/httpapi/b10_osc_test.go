//go:build integration

// b10_osc_test.go integration-side fake gh-ost binary builder and OscRunner adapter
// (shares testdata fake source with osc package unit tests; pinned sha256 semantics enforced).
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	"github.com/cookieY/YearningX/backend/internal/osc"
)

// buildFakeGhostShared compiles fake binary and returns (path, sha256).
func buildFakeGhostShared(t *testing.T) (string, string) {
	t.Helper()
	src, err := filepath.Abs("../../internal/osc/testdata/fakeghost")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fake-gh-ost")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("build fake ghost: %v\n%s", berr, out)
	}
	data, rerr := os.ReadFile(bin)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// oscRunnerAdapter adapts osc.Runner to the application-layer OscRunner interface.
type oscRunnerAdapter struct{ r *osc.Runner }

func (a *oscRunnerAdapter) Run(ctx context.Context, job *osc.Job, hooks osc.Hooks) (*osc.Outcome, error) {
	return a.r.Run(ctx, job, hooks)
}

func (a *oscRunnerAdapter) Cancel(dir string) { a.r.Cancel(dir) }

func newOscRunnerAdapter() execapp.OscRunner { return &oscRunnerAdapter{r: osc.NewRunner()} }
