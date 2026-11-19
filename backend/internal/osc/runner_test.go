//go:build !integration

// runner_test.go tests the gh-ost process adapter: compiles testdata fakeghost as a pinned
// binary (with real SHA-256 verification enabled), covering success, failure, cancellation, process loss,
// residual inspection, and zero credential leakage (argv/env/output) per gh-ost-execution-contract tests.
package osc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildFakeGhost compiles the fake binary and returns (path, sha256).
func buildFakeGhost(t *testing.T) (string, string) {
	t.Helper()
	src, err := filepath.Abs("testdata/fakeghost")
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

func newJob(t *testing.T, bin, sha string, alter string) *Job {
	t.Helper()
	dir := t.TempDir()
	argv, _, err := BuildArgv(PlanInput{
		Database: "shop", Table: "orders", Alter: alter,
		Host: "127.0.0.1", Port: 3306, User: "exec_user",
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Job{
		BinaryPath: bin, BinarySHA256: sha, Argv: argv, Dir: dir,
		Password: "f4ke-pw!", Inspector: &fakeInspector{},
		Database: "shop", Table: "orders",
	}
}

func TestRunnerSuccessProgressAndHeartbeat(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int")
	var beats, progs int64
	r := NewRunner()
	out, err := r.Run(context.Background(), job, Hooks{
		OnHeartbeat: func(time.Time) { atomic.AddInt64(&beats, 1) },
		OnProgress:  func(string, int, int64) { atomic.AddInt64(&progs, 1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Phase != PhaseSucceeded || out.ExitCode != 0 {
		t.Fatalf("phase=%s exit=%d", out.Phase, out.ExitCode)
	}
	if out.ProgressBps != 10000 || out.RowsCopied != 100 {
		t.Fatalf("progress=%d rows=%d", out.ProgressBps, out.RowsCopied)
	}
	if out.ResidualState != ResidualNone {
		t.Fatalf("residual=%s", out.ResidualState)
	}
	if beats == 0 || progs == 0 {
		t.Fatalf("callbacks not fired: beats=%d progs=%d", beats, progs)
	}
}

func TestRunnerCredentialsNeverLeak(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int MODE_DUMP")
	out, err := NewRunner().Run(context.Background(), job, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	dump, rerr := os.ReadFile(filepath.Join(job.Dir, "fakeghost-dump"))
	if rerr != nil {
		t.Fatal("dump missing")
	}
	text := string(dump)
	// argv surface: --ask-pass is present and the password never appears.
	if !strings.Contains(text, "--ask-pass") {
		t.Fatal("ask-pass flag missing from argv dump")
	}
	for _, leak := range []string{"f4ke-pw!", "--password"} {
		if strings.Contains(text, leak) {
			t.Fatalf("credential leaked to %q", strings.SplitN(text, "\n", 2)[0])
		}
	}
	// env surface: password must never enter the process environment.
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "ENV ") && strings.Contains(line, "hunter2") {
			t.Fatal("credential leaked into environment")
		}
	}
	// output surface: redacted tail must not contain the password.
	if strings.Contains(out.StdoutTail, "hunter2") {
		t.Fatal("credential leaked into parsed output tail")
	}
}

func TestRunnerExitNonzeroFails(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int MODE_FAIL")
	out, err := NewRunner().Run(context.Background(), job, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Phase != PhaseFailed || out.ExitCode != 1 {
		t.Fatalf("phase=%s exit=%d", out.Phase, out.ExitCode)
	}
	if out.FailureName == "" {
		t.Fatal("failure name required")
	}
}

func TestRunnerCancelByPanicFlag(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int MODE_HANG")
	r := NewRunner()
	go func() {
		time.Sleep(300 * time.Millisecond)
		r.Cancel(job.Dir)
	}()
	start := time.Now()
	out, err := r.Run(context.Background(), job, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Phase != PhaseCancelled || !out.CancelObserved {
		t.Fatalf("phase=%s cancelObserved=%v", out.Phase, out.CancelObserved)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("cancel must terminate promptly")
	}
	if _, serr := os.Stat(filepath.Join(job.Dir, "panic-flag")); serr != nil {
		t.Fatal("panic flag must be created on cancel")
	}
}

func TestRunnerProcessLostOnParentCancel(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int MODE_HANG")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	out, err := NewRunner().Run(ctx, job, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.ProcessLost || out.Phase != PhaseResultUnknown {
		t.Fatalf("processLost=%v phase=%s", out.ProcessLost, out.Phase)
	}
}

func TestRunnerResidualDetected(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int")
	job.Inspector = &fakeInspector{ghost: true}
	out, err := NewRunner().Run(context.Background(), job, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.ResidualState != ResidualDetected {
		t.Fatalf("residual=%s want detected", out.ResidualState)
	}
}

func TestRunnerBinaryHashMismatch(t *testing.T) {
	bin, _ := buildFakeGhost(t)
	job := newJob(t, bin, strings.Repeat("0", 64), "ADD COLUMN c1 int")
	if _, err := NewRunner().Run(context.Background(), job, Hooks{}); err == nil {
		t.Fatal("hash mismatch must fail spawn")
	}
}

func TestRunnerAttemptDirPermissions(t *testing.T) {
	bin, sha := buildFakeGhost(t)
	job := newJob(t, bin, sha, "ADD COLUMN c1 int")
	dir := job.Dir + "/nested"
	job.Dir = dir
	// Control file paths in argv are generated by BuildArgv; rebuild here to point to the new directory.
	argv, _, err := BuildArgv(PlanInput{
		Database: "shop", Table: "orders", Alter: "ADD COLUMN c1 int",
		Host: "127.0.0.1", Port: 3306, User: "exec_user",
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	job.Argv = argv
	if _, err := NewRunner().Run(context.Background(), job, Hooks{}); err != nil {
		t.Fatal(err)
	}
	info, serr := os.Stat(dir)
	if serr != nil {
		t.Fatal(serr)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("attempt dir perm=%v want 0700", info.Mode().Perm())
	}
}
