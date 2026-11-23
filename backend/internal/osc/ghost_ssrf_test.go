// ghost_ssrf_test.go verifies built-in gh-ost runner defense-in-depth (validateInProcessGhOstDestinations):
// rejects unpinned hostnames, missing AssumeMasterHostname, and forbidden target/master IPs BEFORE touching
// any TCP socket (decoy listener accept count remains 0).
package osc

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestInProcessGhOstRejectsUnpinnedOrForbiddenDestinationsWithoutConnecting(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(false))

	decoy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decoy.Close() }()
	var accepts atomic.Int32
	go func() {
		for {
			c, aerr := decoy.Accept()
			if aerr != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()
	_, portStr, err := net.SplitHostPort(decoy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	r := NewRunner()
	validArgv := []string{"gh-ost", "--host=10.0.0.5", "--port=" + portStr, "--user=yr", "--database=demo", "--table=orders", "--alter=ADD COLUMN c INT"}

	t.Run("rejects unpinned hostname target", func(t *testing.T) {
		job := &Job{
			BinaryPath: BuiltinInProcessGhOstPath, BinarySHA256: BuiltinInProcessGhOstSHA256,
			Tool: ToolGhOst, Argv: []string{"gh-ost", "--host=db.internal", "--port=" + portStr, "--user=yr", "--database=demo", "--table=orders", "--alter=ADD COLUMN c INT"},
			PinnedHost: "", AssumeMasterHostname: "10.0.0.5:" + portStr,
			Dir: t.TempDir(), Database: "demo", Table: "orders",
		}
		if _, err := r.Run(context.Background(), job, Hooks{}); err == nil || !strings.Contains(err.Error(), "unpinned") {
			t.Fatalf("got %v; want unpinned hostname error", err)
		}
	})

	t.Run("rejects empty AssumeMasterHostname", func(t *testing.T) {
		job := &Job{
			BinaryPath: BuiltinInProcessGhOstPath, BinarySHA256: BuiltinInProcessGhOstSHA256,
			Tool: ToolGhOst, Argv: validArgv,
			PinnedHost: "10.0.0.5", AssumeMasterHostname: "",
			Dir: t.TempDir(), Database: "demo", Table: "orders",
		}
		if _, err := r.Run(context.Background(), job, Hooks{}); err == nil || !strings.Contains(err.Error(), "assume_master_hostname") {
			t.Fatalf("got %v; want assume_master_hostname pinned error", err)
		}
	})

	t.Run("rejects forbidden pinned target IP (127.0.0.1)", func(t *testing.T) {
		job := &Job{
			BinaryPath: BuiltinInProcessGhOstPath, BinarySHA256: BuiltinInProcessGhOstSHA256,
			Tool: ToolGhOst, Argv: []string{"gh-ost", "--host=127.0.0.1", "--port=" + portStr, "--user=yr", "--database=demo", "--table=orders", "--alter=ADD COLUMN c INT"},
			PinnedHost: "127.0.0.1", AssumeMasterHostname: "10.0.0.5:3306",
			Dir: t.TempDir(), Database: "demo", Table: "orders",
		}
		if _, err := r.Run(context.Background(), job, Hooks{}); err == nil || !strings.Contains(err.Error(), "target address forbidden") {
			t.Fatalf("got %v; want target address forbidden", err)
		}
	})

	t.Run("rejects forbidden AssumeMasterHostname IP (169.254.169.254 or 127.0.0.1)", func(t *testing.T) {
		for _, badMaster := range []string{"169.254.169.254:3306", "127.0.0.1:" + portStr, "rogue.master:3306"} {
			job := &Job{
				BinaryPath: BuiltinInProcessGhOstPath, BinarySHA256: BuiltinInProcessGhOstSHA256,
				Tool: ToolGhOst, Argv: validArgv,
				PinnedHost: "10.0.0.5", AssumeMasterHostname: badMaster,
				Dir: t.TempDir(), Database: "demo", Table: "orders",
			}
			if _, err := r.Run(context.Background(), job, Hooks{}); err == nil {
				t.Fatalf("AssumeMasterHostname=%s unexpectedly accepted", badMaster)
			}
		}
	})

	if got := accepts.Load(); got != 0 {
		t.Fatalf("decoy socket accepted %d connections; want 0", got)
	}
}
