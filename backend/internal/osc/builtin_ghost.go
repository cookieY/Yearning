package osc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/github/gh-ost/go/base"
	"github.com/github/gh-ost/go/logic"
	"github.com/github/gh-ost/go/metrics"
	"github.com/github/gh-ost/go/sql"
	"github.com/openark/golib/log"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// BuiltinGhOstVersion identifies the embedded github.com/github/gh-ost library version.
const BuiltinGhOstVersion = "builtin-gh-ost-v1.1.11"

// BuiltinInProcessGhOstPath is the sentinel binary path for direct in-process Go function execution.
const BuiltinInProcessGhOstPath = "builtin:gh-ost"

// BuiltinInProcessGhOstSHA256 is the deterministic 64-character SHA-256 hex identity for BuiltinGhOstVersion.
var BuiltinInProcessGhOstSHA256 = func() string {
	sum := sha256.Sum256([]byte(BuiltinGhOstVersion))
	return hex.EncodeToString(sum[:])
}()

var inProcessReplicaServerSeq uint32

// inProcessHooks implements base.Hooks to receive direct in-process lifecycle callbacks from logic.Migrator.
type inProcessHooks struct {
	mu       sync.Mutex
	mctx     *base.MigrationContext
	out      *Outcome
	hooks    Hooks
	tailBuf  []byte
	lastBeat time.Time
	lastProg time.Time
}

func newInProcessHooks(mctx *base.MigrationContext, out *Outcome, hooks Hooks) *inProcessHooks {
	return &inProcessHooks{
		mctx:  mctx,
		out:   out,
		hooks: hooks,
	}
}

func (h *inProcessHooks) emitBeatLocked(now time.Time, force bool) {
	if h.hooks.OnHeartbeat == nil {
		return
	}
	if force || h.lastBeat.IsZero() || now.Sub(h.lastBeat) >= time.Second {
		h.lastBeat = now
		h.hooks.OnHeartbeat(now)
	}
}

func (h *inProcessHooks) computeProgressLocked() (int, int64) {
	copied := h.mctx.GetTotalRowsCopied()
	est := atomic.LoadInt64(&h.mctx.RowsEstimate) + atomic.LoadInt64(&h.mctx.RowsDeltaEstimate)
	bps := h.out.ProgressBps
	if est > 0 {
		bps = int(copied * 10000 / est)
		if bps > 10000 {
			bps = 10000
		}
	}
	return bps, copied
}

func (h *inProcessHooks) setPhaseLocked(phase string, forceBps int, forceEmit bool) {
	now := time.Now()
	h.emitBeatLocked(now, forceEmit)
	bps, copied := h.computeProgressLocked()
	if forceBps >= 0 {
		bps = forceBps
	}
	h.out.Phase = phase
	h.out.ProgressBps = bps
	h.out.RowsCopied = copied
	if h.hooks.OnProgress != nil && (forceEmit || h.lastProg.IsZero() || now.Sub(h.lastProg) >= time.Second) {
		h.lastProg = now
		h.hooks.OnProgress(phase, bps, copied)
	}
}

func (h *inProcessHooks) sampleTick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.emitBeatLocked(now, false)
	bps, copied := h.computeProgressLocked()
	phase := h.out.Phase
	switch {
	case atomic.LoadInt64(&h.mctx.CutOverCompleteFlag) != 0:
		phase = PhaseCleanup
		bps = 10000
	case atomic.LoadInt64(&h.mctx.InCutOverCriticalSectionFlag) != 0:
		phase = PhaseCutover
		bps = 10000
	case atomic.LoadInt64(&h.mctx.IsPostponingCutOver) != 0:
		phase = PhasePostponedCutover
	case phase == PhasePreflight && copied > 0:
		phase = PhaseCopying
	}
	prevPhase, prevBps, prevCopied := h.out.Phase, h.out.ProgressBps, h.out.RowsCopied
	h.out.Phase = phase
	h.out.ProgressBps = bps
	h.out.RowsCopied = copied
	progressChanged := phase != prevPhase || bps != prevBps || copied != prevCopied
	if h.hooks.OnProgress != nil && (h.lastProg.IsZero() || now.Sub(h.lastProg) >= time.Second || (progressChanged && now.Sub(h.lastProg) >= 250*time.Millisecond)) {
		h.lastProg = now
		h.hooks.OnProgress(phase, bps, copied)
	}
}

func (h *inProcessHooks) appendTailLocked(line string) {
	h.tailBuf = append(h.tailBuf, []byte(redact(line)+"\n")...)
	if len(h.tailBuf) > 4096 {
		h.tailBuf = h.tailBuf[len(h.tailBuf)-4096:]
	}
}

func (h *inProcessHooks) tail() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(h.tailBuf)
}

func (h *inProcessHooks) OnStartup() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhasePreflight, 0, true)
	return nil
}

func (h *inProcessHooks) OnValidated() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhasePreflight, 0, false)
	return nil
}

func (h *inProcessHooks) OnRowCountComplete() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitBeatLocked(time.Now(), false)
	return nil
}

func (h *inProcessHooks) OnBeforeRowCopy() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhaseCopying, -1, true)
	return nil
}

func (h *inProcessHooks) OnRowCopyComplete() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhaseCopying, 10000, true)
	return nil
}

func (h *inProcessHooks) OnBeginPostponed() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhasePostponedCutover, -1, true)
	return nil
}

func (h *inProcessHooks) OnBeforeCutOver() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhaseCutover, 10000, true)
	return nil
}

func (h *inProcessHooks) OnInteractiveCommand(_ string) error { return nil }

func (h *inProcessHooks) OnSuccess(_ bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setPhaseLocked(PhaseCleanup, 10000, true)
	return nil
}

func (h *inProcessHooks) OnFailure() error { return nil }

func (h *inProcessHooks) OnBatchCopyRetry(errorMessage string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendTailLocked(errorMessage)
	return nil
}

func (h *inProcessHooks) OnStatus(statusMessage string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendTailLocked(statusMessage)
	h.setPhaseLocked(h.out.Phase, -1, false)
	return nil
}

func (h *inProcessHooks) OnStopReplication() error  { return nil }
func (h *inProcessHooks) OnStartReplication() error { return nil }

// runInProcessGhOst executes gh-ost directly in-process via Go function calls on base.MigrationContext
// and logic.Migrator without spawning an OS subprocess, piping stdin, or regex-scraping stdout.
func (r *Runner) runInProcessGhOst(ctx context.Context, job *Job, hooks Hooks) (*Outcome, error) {
	if job.Tool == ToolPtOsc {
		return nil, fmt.Errorf("osc: pt-osc is not supported")
	}
	if len(job.BinarySHA256) == 64 && job.BinarySHA256 != BuiltinInProcessGhOstSHA256 {
		return nil, fmt.Errorf("osc: binary hash mismatch")
	}
	if err := os.MkdirAll(job.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("osc: attempt dir: %w", err)
	}

	password := job.Password
	job.Password = ""

	migrationContext := base.NewMigrationContext()
	migrationContext.Log.SetLevel(log.ERROR)

	if err := applyJobToMigrationContext(migrationContext, job, password); err != nil {
		return nil, fmt.Errorf("osc: invalid in-process gh-ost config: %w", err)
	}
	password = ""
	if err := validateInProcessGhOstDestinations(migrationContext); err != nil {
		return nil, err
	}

	if metricsClient, merr := metrics.NewClient("", nil, "gh_ost."); merr == nil {
		defer func() { _ = metricsClient.Close() }()
		migrationContext.Metrics = metricsClient
	}
	defer func() {
		if migrationContext.ServeSocketFile != "" {
			_ = os.Remove(migrationContext.ServeSocketFile)
		}
	}()

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

	out := &Outcome{
		Phase:         PhasePreflight,
		ResidualState: ResidualNotChecked,
		ProgressBps:   0,
		RowsCopied:    0,
	}
	ipHooks := newInProcessHooks(migrationContext, out, hooks)
	migrationContext.Hooks = ipHooks

	doneCh := make(chan struct{})
	var monitorWG sync.WaitGroup
	monitorWG.Add(2)

	// Propagate in-memory cancellation (Runner.Cancel or parent context shutdown) directly to MigrationContext.
	go func() {
		defer monitorWG.Done()
		select {
		case <-doneCh:
			return
		case <-runCtx.Done():
			abortErr := fmt.Errorf("migration cancelled")
			migrationContext.SetAbortError(abortErr)
			_ = base.SendWithContext(migrationContext.GetContext(), migrationContext.PanicAbort, abortErr)
			migrationContext.CancelContext()
		}
	}()

	// Sample in-memory progress and heartbeats directly from MigrationContext atomic counters.
	go func() {
		defer monitorWG.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-doneCh:
				return
			case <-ticker.C:
				ipHooks.sampleTick()
			}
		}
	}()

	migrator := logic.NewMigrator(migrationContext, BuiltinGhOstVersion)
	migrateErr := migrator.Migrate()
	close(doneCh)
	monitorWG.Wait()

	cancelRequested := runCtx.Err() != nil && ctx.Err() == nil
	if cancelRequested {
		out.CancelObserved = true
	}

	ipHooks.mu.Lock()
	if migrateErr != nil {
		ipHooks.appendTailLocked(migrateErr.Error())
	}
	switch {
	case migrateErr == nil:
		out.ExitCode = 0
		out.Phase = PhaseSucceeded
		out.ProgressBps = 10000
		out.RowsCopied = migrationContext.GetTotalRowsCopied()
		out.FailureName = ""
	case ctx.Err() != nil:
		out.ProcessLost = true
		out.Phase = PhaseResultUnknown
		out.FailureName = "OSC_PROCESS_LOST"
	case out.CancelObserved:
		out.ExitCode = 1
		out.Phase = PhaseCancelled
		out.FailureName = "OSC_CANCELLED"
	default:
		out.ExitCode = 1
		if out.Phase != PhaseCutover && out.Phase != PhaseCleanup {
			out.Phase = PhaseFailed
		}
		out.FailureName = "OSC_EXIT_NONZERO"
	}
	finalPhase := out.Phase
	finalBps := out.ProgressBps
	finalRows := out.RowsCopied
	ipHooks.mu.Unlock()

	if hooks.OnProgress != nil {
		hooks.OnProgress(finalPhase, finalBps, finalRows)
	}

	if migrationContext.ServeSocketFile != "" {
		_ = os.Remove(migrationContext.ServeSocketFile)
	}

	if job.Inspector != nil {
		ictx, icancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		ghost, changelog, triggers, sock, ierr := job.Inspector.Inspect(ictx, job.Database, job.Table)
		icancel()
		if ierr != nil {
			out.ResidualState = ResidualNotChecked
			out.StdoutTail = ipHooks.tail()
			return out, nil
		}
		sockPath := job.ServeSocket
		if sockPath == "" {
			sockPath = migrationContext.ServeSocketFile
		}
		if sockPath != "" {
			_, statErr := os.Stat(sockPath)
			sock = sock || statErr == nil
		}
		if ghost || changelog || triggers || sock {
			out.ResidualState = ResidualDetected
		} else {
			out.ResidualState = ResidualNone
		}
	}
	out.StdoutTail = ipHooks.tail()
	return out, nil
}

// applyJobToMigrationContext populates base.MigrationContext directly from Job fields and parsed flags.
func applyJobToMigrationContext(migrationContext *base.MigrationContext, job *Job, password string) error {
	fs := flag.NewFlagSet("gh-ost-inprocess", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&migrationContext.InspectorConnectionConfig.Key.Hostname, "host", "127.0.0.1", "")
	fs.IntVar(&migrationContext.InspectorConnectionConfig.Key.Port, "port", 3306, "")
	fs.StringVar(&migrationContext.CliUser, "user", "", "")
	fs.StringVar(&migrationContext.DatabaseName, "database", job.Database, "")
	fs.StringVar(&migrationContext.OriginalTableName, "table", job.Table, "")
	fs.StringVar(&migrationContext.AlterStatement, "alter", "", "")

	var askPass bool
	var executeFlag bool
	var exactRowCount bool
	var concurrentRowCount bool
	var okToDropTable bool
	var timestampOldTable bool
	var cutOverExponentialBackoff bool
	var attemptInstantDDL bool
	var cutOver string
	var panicFlagFile string
	var postponeCutOverFlagFile string
	var serveSocketFile string
	var serveTCPPort int64
	var heartbeatIntervalMillis int64
	var chunkSize int64
	var dmlBatchSize int64
	var maxLagMillis int64
	var defaultRetries int64
	var niceRatio float64
	var cutOverLockTimeout int64
	var exponentialBackoffMaxInterval int64
	var criticalLoadIntervalMillis int64
	var criticalLoadHibernateSeconds int64
	var maxLoad string
	var criticalLoad string

	fs.BoolVar(&askPass, "ask-pass", false, "")
	fs.BoolVar(&executeFlag, "execute", true, "")
	fs.BoolVar(&migrationContext.AssumeRBR, "assume-rbr", true, "")
	fs.BoolVar(&exactRowCount, "exact-rowcount", true, "")
	fs.BoolVar(&concurrentRowCount, "concurrent-rowcount", false, "")
	fs.BoolVar(&okToDropTable, "ok-to-drop-table", true, "")
	fs.BoolVar(&timestampOldTable, "timestamp-old-table", false, "")
	fs.BoolVar(&cutOverExponentialBackoff, "cut-over-exponential-backoff", false, "")
	fs.BoolVar(&attemptInstantDDL, "attempt-instant-ddl", false, "")
	fs.StringVar(&cutOver, "cut-over", "atomic", "")
	fs.StringVar(&panicFlagFile, "panic-flag-file", "", "")
	fs.StringVar(&postponeCutOverFlagFile, "postpone-cut-over-flag-file", "", "")
	fs.StringVar(&serveSocketFile, "serve-socket-file", "", "")
	fs.Int64Var(&serveTCPPort, "serve-tcp-port", 0, "")
	fs.Int64Var(&heartbeatIntervalMillis, "heartbeat-interval-millis", 100, "")
	fs.Int64Var(&chunkSize, "chunk-size", 1000, "")
	fs.Int64Var(&dmlBatchSize, "dml-batch-size", 10, "")
	fs.Int64Var(&maxLagMillis, "max-lag-millis", 1500, "")
	fs.Int64Var(&defaultRetries, "default-retries", 60, "")
	fs.Float64Var(&niceRatio, "nice-ratio", 0, "")
	fs.Int64Var(&cutOverLockTimeout, "cut-over-lock-timeout-seconds", 3, "")
	fs.Int64Var(&exponentialBackoffMaxInterval, "exponential-backoff-max-interval", 64, "")
	fs.Int64Var(&criticalLoadIntervalMillis, "critical-load-interval-millis", 0, "")
	fs.Int64Var(&criticalLoadHibernateSeconds, "critical-load-hibernate-seconds", 0, "")
	fs.StringVar(&maxLoad, "max-load", "", "")
	fs.StringVar(&criticalLoad, "critical-load", "", "")

	args := job.Argv
	if len(args) > 0 && args[0] == "gh-ost" {
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if migrationContext.DatabaseName == "" {
		migrationContext.DatabaseName = job.Database
	}
	if migrationContext.OriginalTableName == "" {
		migrationContext.OriginalTableName = job.Table
	}
	if strings.TrimSpace(job.PinnedHost) != "" {
		migrationContext.InspectorConnectionConfig.Key.Hostname = strings.TrimSpace(job.PinnedHost)
	}
	if strings.TrimSpace(job.AssumeMasterHostname) != "" {
		migrationContext.AssumeMasterHostname = strings.TrimSpace(job.AssumeMasterHostname)
	}
	migrationContext.CliPassword = password

	if postponeCutOverFlagFile != "" && base.FileExists(postponeCutOverFlagFile) {
		migrationContext.PostponeCutOverFlagFile = postponeCutOverFlagFile
	}
	if panicFlagFile != "" {
		migrationContext.PanicFlagFile = panicFlagFile
	}
	// Keep Unix domain socket path short (< 104 bytes on darwin/macOS) inside a 0700 owner-only directory.
	shortUUID := strings.ReplaceAll(migrationContext.Uuid, "-", "")
	if len(shortUUID) > 12 {
		shortUUID = shortUUID[:12]
	}
	sockDir := filepath.Join(os.TempDir(), fmt.Sprintf("yx-osc-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return fmt.Errorf("create osc socket dir: %w", err)
	}
	_ = os.Chmod(sockDir, 0o700)
	migrationContext.ServeSocketFile = filepath.Join(sockDir, fmt.Sprintf("gh-%s.sock", shortUUID))
	migrationContext.DropServeSocket = true

	parser := sql.NewParserFromAlterStatement(migrationContext.AlterStatement)
	migrationContext.AlterStatementOptions = parser.GetAlterStatementOptions()

	seq := atomic.AddUint32(&inProcessReplicaServerSeq, 1)
	replicaServerID := uint((uint32(os.Getpid())%800000)+100000) + uint(seq%90000)

	migrationContext.Noop = false
	migrationContext.CutOverType = base.CutOverAtomic
	migrationContext.AllowedRunningOnMaster = true
	migrationContext.AllowedMasterMaster = true
	migrationContext.OkToDropTable = okToDropTable
	migrationContext.TimestampOldTable = timestampOldTable
	migrationContext.CutOverExponentialBackoff = cutOverExponentialBackoff
	migrationContext.AttemptInstantDDL = attemptInstantDDL
	migrationContext.CriticalLoadIntervalMilliseconds = criticalLoadIntervalMillis
	migrationContext.CriticalLoadHibernateSeconds = criticalLoadHibernateSeconds
	migrationContext.InitiallyDropGhostTable = true
	migrationContext.InitiallyDropOldTable = true
	migrationContext.SkipMetadataLockCheck = true
	migrationContext.SkipPortValidation = true
	migrationContext.ApproveRenamedColumns = true
	migrationContext.NullableUniqueKeyAllowed = true
	migrationContext.AllowZeroInDate = true
	migrationContext.CountTableRows = exactRowCount
	migrationContext.ConcurrentCountTableRows = concurrentRowCount
	migrationContext.ReplicaServerId = replicaServerID
	migrationContext.ThrottleHTTPIntervalMillis = 100
	migrationContext.ThrottleHTTPTimeoutMillis = 1000
	migrationContext.HooksStatusIntervalSec = 1
	migrationContext.CheckpointIntervalSeconds = 300

	if heartbeatIntervalMillis <= 0 {
		heartbeatIntervalMillis = 100
	}
	if exponentialBackoffMaxInterval <= 0 {
		exponentialBackoffMaxInterval = 64
	}
	migrationContext.SetHeartbeatIntervalMilliseconds(heartbeatIntervalMillis)
	migrationContext.SetChunkSize(chunkSize)
	migrationContext.SetDMLBatchSize(dmlBatchSize)
	migrationContext.SetMaxLagMillisecondsThrottleThreshold(maxLagMillis)
	migrationContext.SetNiceRatio(niceRatio)
	migrationContext.SetDefaultNumRetries(defaultRetries)
	_ = migrationContext.SetCutOverLockTimeoutSeconds(cutOverLockTimeout)
	_ = migrationContext.SetExponentialBackoffMaxInterval(exponentialBackoffMaxInterval)

	if maxLoad != "" {
		if err := migrationContext.ReadMaxLoad(maxLoad); err != nil {
			return err
		}
	}
	if criticalLoad != "" {
		if err := migrationContext.ReadCriticalLoad(criticalLoad); err != nil {
			return err
		}
	}

	migrationContext.ApplyCredentials()
	migrationContext.SetConnectionCharset("utf8mb4,utf8,latin1")
	return migrationContext.SetConnectionConfig("innodb")
}

// validateInProcessGhOstDestinations enforces defense-in-depth before gh-ost opens any TCP/binlog socket:
// both the target inspector host and AssumeMasterHostname must be IP literals validated by addrguard,
// guaranteeing gh-ost neither re-resolves hostnames nor discovers an arbitrary Master_Host via SHOW SLAVE STATUS.
func validateInProcessGhOstDestinations(mctx *base.MigrationContext) error {
	targetHost := strings.TrimSpace(mctx.InspectorConnectionConfig.Key.Hostname)
	targetIP := net.ParseIP(strings.Trim(targetHost, "[]"))
	if targetIP == nil {
		return fmt.Errorf("osc: unpinned target hostname rejected")
	}
	if err := addrguard.ValidateIPFor(addrguard.PurposeOSC, targetIP); err != nil {
		return fmt.Errorf("osc: target address forbidden: %w", err)
	}
	if err := addrguard.CheckSelfTargetFor(addrguard.PurposeOSC, targetIP, mctx.InspectorConnectionConfig.Key.Port, ""); err != nil {
		return fmt.Errorf("osc: target address forbidden: %w", err)
	}
	assumeRaw := strings.TrimSpace(mctx.AssumeMasterHostname)
	if assumeRaw == "" {
		return fmt.Errorf("osc: assume_master_hostname must be pinned before launch")
	}
	assumeHost, assumePortStr, err := net.SplitHostPort(assumeRaw)
	if err != nil {
		return fmt.Errorf("osc: assume_master_hostname must be ip:port")
	}
	assumePort, perr := strconv.Atoi(assumePortStr)
	assumeIP := net.ParseIP(strings.Trim(assumeHost, "[]"))
	if perr != nil || assumePort < 1 || assumePort > 65535 || assumeIP == nil {
		return fmt.Errorf("osc: assume_master_hostname must contain a valid ip:port")
	}
	if err := addrguard.ValidateIPFor(addrguard.PurposeOSC, assumeIP); err != nil {
		return fmt.Errorf("osc: master address forbidden: %w", err)
	}
	if err := addrguard.CheckSelfTargetFor(addrguard.PurposeOSC, assumeIP, assumePort, ""); err != nil {
		return fmt.Errorf("osc: master address forbidden: %w", err)
	}
	return nil
}
