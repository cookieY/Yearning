// runner.go implements the gh-ost process adapter (per gh-ost-execution-contract process authority):
// explicit argv via exec.CommandContext, dedicated ask-pass stdin pipe (passwords never enter
// argv/env/logs), 0700 attempt directory, panic-flag cancellation, bounded redacted output parsing,
// heartbeat and progress callbacks, exit code classification, and residual resource inspection.
// Tickets do not auto-resume after process failure or loss (enforced by caller); this package only reports observed facts.
package osc

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Outcome records the terminal observed facts of a single process execution.
type Outcome struct {
	Phase          string // Contract lifecycle phases
	ExitCode       int
	ProcessLost    bool // Process exited unexpectedly without normal completion (killed, crashed, or wait failed)
	ProgressBps    int  // 0..10000
	RowsCopied     int64
	ResidualState  string // not_checked|none|detected|cleanup_required|cleaned
	FailureName    string // Redacted failure name (excludes credentials and raw output)
	CancelObserved bool
	StdoutTail     string // Redacted bounded output tail (for troubleshooting; <= 4 KiB)
}

// Phase constants (contract lifecycle.phases).
const (
	PhasePlanned          = "planned"
	PhasePreflight        = "preflight"
	PhaseCopying          = "copying"
	PhasePostponedCutover = "postponed_cutover"
	PhaseCutover          = "cutover"
	PhaseCleanup          = "cleanup"
	PhaseSucceeded        = "succeeded"
	PhaseFailed           = "failed"
	PhaseCancelled        = "cancelled"
	PhaseResultUnknown    = "result_unknown"
)

// ResidualState constants.
const (
	ResidualNotChecked = "not_checked"
	ResidualNone       = "none"
	ResidualDetected   = "detected"
	ResidualCleanupReq = "cleanup_required"
	ResidualCleaned    = "cleaned"
)

// ResidualInspector inspects residual artifacts in the target database
// (information_schema queries executed by caller via execution pool; this package performs no DB I/O).
type ResidualInspector interface {
	// Inspect returns (ghostTableExists, changelogTableExists, targetTableTriggerCount, socketFileExists).
	Inspect(ctx context.Context, database, table string) (ghost, changelog, triggers bool, socketExists bool, err error)
}

// Hooks holds runtime callbacks (nil-safe).
type Hooks struct {
	// OnHeartbeat reports process liveness heartbeats (persisted by caller with <= 10s throttling).
	OnHeartbeat func(at time.Time)
	// OnProgress reports observed copy progress.
	OnProgress func(phase string, progressBps int, rowsCopied int64)
}

// Job defines the input for a single process execution. Password is piped via stdin and never enters argv/env.
type Job struct {
	BinaryPath           string
	BinarySHA256         string // 64-character hex string; verified before spawn
	ToolVersion          string
	Tool                 Tool     // gh-ost|pt-osc (cancellation signal and progress semantics depend on tool)
	Argv                 []string // Output of Build*Argv
	PinnedHost           string   // Validated IP literal pinned by preflight (never re-resolved inside gh-ost)
	AssumeMasterHostname string   // Explicit ip:port disabling gh-ost SHOW SLAVE STATUS master discovery
	Dir                  string   // 0700 attempt directory
	ServeSocket          string
	Password             string
	// When Inspector is nil, residual state defaults to not_checked (acceptable only on the success path).
	Inspector         ResidualInspector
	Database          string
	Table             string
	StderrPassthrough bool // For test observation; false in production
}

// Runner executes a gh-ost process.
type Runner struct {
	mu sync.Mutex
	// cancelById tracks running process cancellation handles (Cancel addresses by attempt directory panic-flag path).
	running map[string]context.CancelFunc
}

// NewRunner constructs a process Runner.
func NewRunner() *Runner {
	return &Runner{running: map[string]context.CancelFunc{}}
}

// Cancel creates the panic flag file and cancels the context (tool-native stop protocol).
func (r *Runner) Cancel(dir string) {
	r.mu.Lock()
	cancel := r.running[dir]
	r.mu.Unlock()
	_ = os.WriteFile(filepath.Join(dir, "panic-flag"), []byte("cancel"), 0o600)
	if cancel != nil {
		cancel()
	}
}

// Run executes gh-ost directly in-process via Go function calls when BinaryPath is empty or
// BuiltinInProcessGhOstPath, or runs an external pinned binary when explicitly configured for testing.
func (r *Runner) Run(ctx context.Context, job *Job, hooks Hooks) (*Outcome, error) {
	if job.BinaryPath == "" || job.BinaryPath == BuiltinInProcessGhOstPath {
		return r.runInProcessGhOst(ctx, job, hooks)
	}
	if err := verifyBinaryHash(job.BinaryPath, job.BinarySHA256); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(job.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("osc: attempt dir: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.running[job.Dir] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, job.Dir)
		r.mu.Unlock()
	}()

	out := &Outcome{Phase: PhasePreflight, ResidualState: ResidualNotChecked, ProgressBps: 0, RowsCopied: 0}
	cmd := exec.CommandContext(runCtx, job.BinaryPath, job.Argv...)
	cmd.Dir = job.Dir
	// Minimize environment: inherit only PATH/HOME/TZ and strip everything else (reducing credential leakage surface).
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + job.Dir, "TZ=UTC"}
	cmd.WaitDelay = 30 * time.Second
	if job.Tool == ToolPtOsc {
		// pt-osc cancellation protocol: send SIGTERM (giving it a chance to clean up triggers/shadow tables),
		// then SIGKILL if still alive after a 15s grace period (overriding default WaitDelay).
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			return nil
		}
		cmd.WaitDelay = 15 * time.Second
	}
	// Dedicated ask-pass stdin pipe: write the password once and close immediately (read on gh-ost prompt).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("osc: spawn failed")
	}
	// Password is sent only as bytes over the stdin pipe, and its reference is dropped immediately afterward.
	if _, werr := io.WriteString(stdin, job.Password+"\n"); werr != nil {
		_ = stdin.Close()
	}
	job.Password = ""
	_ = stdin.Close()

	// Bounded output parsing (redaction + progress + heartbeat).
	parser := newOutputParser(out, hooks)
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() { defer wg.Done(); parser.consume(stdoutPipe) }()
	go func() { defer wg.Done(); parser.consume(stderrPipe) }()
	waitErr := cmd.Wait()
	wg.Wait()
	parser.finish()

	// Classify process exit status.
	cancelRequested := runCtx.Err() != nil && ctx.Err() == nil
	if cancelRequested {
		out.CancelObserved = true
	}
	switch {
	case waitErr == nil:
		out.ExitCode = 0
		out.Phase = PhaseSucceeded
		out.FailureName = ""
	case ctx.Err() != nil:
		// Caller context ended (e.g. Pod shutdown): process was killed, terminal state is unknown.
		out.ProcessLost = true
		out.Phase = PhaseResultUnknown
		out.FailureName = "OSC_PROCESS_LOST"
	default:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			out.ExitCode = exitErr.ExitCode()
			if out.CancelObserved {
				out.Phase = PhaseCancelled
				out.FailureName = "OSC_CANCELLED"
			} else {
				// Non-zero exit: attribute failure phase based on whether cutover was reached.
				out.Phase = PhaseFailed
				out.FailureName = "OSC_EXIT_NONZERO"
			}
		} else {
			// Wait error (e.g. signal reaping failure): treat as process lost.
			out.ProcessLost = true
			out.Phase = PhaseResultUnknown
			out.FailureName = "OSC_PROCESS_LOST"
		}
	}
	if hooks.OnProgress != nil {
		hooks.OnProgress(out.Phase, out.ProgressBps, out.RowsCopied)
	}
	// Residual inspection (checks socket/ghost tables even on success without copy side effects;
	// nil Inspector yields not_checked, which is only permissible on exit code 0).
	if job.Inspector != nil {
		ictx, icancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		ghost, changelog, triggers, sock, ierr := job.Inspector.
			Inspect(ictx, job.Database, job.Table)
		icancel()
		// Inspection failure (e.g. target DB unreachable) leaves residual state unknown;
		// failing open as "clean" would mislead manual cleanup, so keep not_checked.
		if ierr != nil {
			out.ResidualState = ResidualNotChecked
			out.StdoutTail = parser.tail()
			return out, nil
		}
		_, statErr := os.Stat(job.ServeSocket)
		sock = sock || statErr == nil
		switch {
		case ghost || changelog || triggers || sock:
			out.ResidualState = ResidualDetected
		default:
			out.ResidualState = ResidualNone
		}
	}
	out.StdoutTail = parser.tail()
	return out, nil
}

// verifyBinaryHash verifies before spawn that the binary SHA-256 matches the pinned release manifest value.
func verifyBinaryHash(path, want string) error {
	if len(want) != 64 {
		return fmt.Errorf("osc: binary hash unpinned")
	}
	f, err := os.Open(path) // #nosec G304 -- path comes from server configuration
	if err != nil {
		return fmt.Errorf("osc: binary unreadable")
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("osc: binary hash unreadable")
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("osc: binary hash mismatch")
	}
	return nil
}

// ---- Bounded output parser ----

var (
	copyRe          = regexp.MustCompile(`Copy:\s+([0-9]+)/([0-9]+)`)
	ptCopyRe        = regexp.MustCompile("Copying `[^`]+`[^:]*:\\s*([0-9]{1,3})%")
	secretRe        = regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*\S+`)
	quotedLiteralRe = regexp.MustCompile(`'[^']*'|"[^"]*"`)
)

// outputParser parses process output: progress, phase, heartbeat, and redacted tail.
type outputParser struct {
	mu       sync.Mutex
	out      *Outcome
	hooks    Hooks
	tailBuf  []byte
	lastBeat time.Time
	lastProg time.Time
}

func newOutputParser(out *Outcome, hooks Hooks) *outputParser {
	return &outputParser{out: out, hooks: hooks, lastBeat: time.Now()}
}

func (p *outputParser) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		p.line(line)
	}
}

func (p *outputParser) line(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	// Heartbeat (any output line counts as activity).
	if p.hooks.OnHeartbeat != nil && now.Sub(p.lastBeat) >= time.Second {
		p.lastBeat = now
		p.hooks.OnHeartbeat(now)
	}
	// Progress (pt-osc "Copying `db`.`tbl`/`db`.`_tbl_new`: 42%" line).
	if m := ptCopyRe.FindStringSubmatch(line); m != nil {
		pct, _ := strconv.Atoi(m[1])
		if pct >= 0 && pct <= 100 {
			bps := pct * 100
			p.out.ProgressBps = bps
			p.out.Phase = PhaseCopying
			if p.hooks.OnProgress != nil && now.Sub(p.lastProg) >= time.Second {
				p.lastProg = now
				p.hooks.OnProgress(PhaseCopying, bps, p.out.RowsCopied)
			}
		}
	}
	// Progress (gh-ost "Copy: 123/456" line).
	if m := copyRe.FindStringSubmatch(line); m != nil {
		done, _ := strconv.ParseInt(m[1], 10, 64)
		total, _ := strconv.ParseInt(m[2], 10, 64)
		if total > 0 {
			bps := int(done * 10000 / total)
			if bps > 10000 {
				bps = 10000
			}
			p.out.ProgressBps = bps
			p.out.RowsCopied = done
			p.out.Phase = PhaseCopying
			if p.hooks.OnProgress != nil && now.Sub(p.lastProg) >= time.Second {
				p.lastProg = now
				p.hooks.OnProgress(PhaseCopying, bps, done)
			}
		}
	}
	// Phase markers (gh-ost output keywords -> contract phase).
	switch {
	case strings.Contains(line, "Waiting for postpone"):
		p.out.Phase = PhasePostponedCutover
	case strings.Contains(line, "CutOver"):
		p.out.Phase = PhaseCutover
	case strings.Contains(line, "Cleanup"):
		p.out.Phase = PhaseCleanup
	}
	// Redacted output tail (last 4 KiB).
	p.tailBuf = append(p.tailBuf, []byte(redact(line)+"\n")...)
	if len(p.tailBuf) > 4096 {
		p.tailBuf = p.tailBuf[len(p.tailBuf)-4096:]
	}
}

func (p *outputParser) finish() {}

func (p *outputParser) tail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.tailBuf)
}

// redact sanitizes output by stripping credential patterns and SQL literal traces.
func redact(line string) string {
	line = secretRe.ReplaceAllString(line, "${1}=***")
	// Uniformly redact quoted literals so residual SQL values do not leak.
	if strings.Count(line, "'") >= 2 || strings.Count(line, `"`) >= 2 {
		line = quotedLiteralRe.ReplaceAllString(line, "'***'")
	}
	return line
}
