// resetadmin.go implements the local recovery command ./Yearning --reset-admin-password
// (01-auth-rbac-prd §4/§6):
//   - Interactive hidden TTY input with confirmation; rejects plaintext password CLI flags
//   - Does not require stopping the service; users row lock ensures only one reset takes effect at a time
//   - Validates password policy, updates Argon2id hash, resets failure counters, and clears lockout
//   - Revokes all admin sessions and records the admin.password_reset_cli audit event
//   - Never outputs plaintext passwords to terminal or logs
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/platform/database"
)

func runResetAdminPassword(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("reset-admin-password", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file (optional)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(ctx, config.ModeServe, *configPath)
	if err != nil {
		return err
	}
	// Explicitly support legacy invocation style: ./Yearning --reset-admin-password
	_ = flags.Args()

	password, err := promptNewPassword()
	if err != nil {
		return err
	}
	pool, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()
	recorder, err := auditlog.NewRecorder(pool.Gorm())
	if err != nil {
		return err
	}
	sessions, err := identity.NewSessionStore(pool.Gorm(), identity.DefaultSessionTTL)
	if err != nil {
		return err
	}
	svc, err := identity.NewService(pool.Gorm(), sessions, nil, recorder)
	if err != nil {
		return err
	}
	if err := svc.ResetAdminPassword(ctx, password, identity.RequestMeta{}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "admin password reset: ok (sessions revoked, lockout cleared, audit written)")
	return nil
}

// promptNewPassword interactively reads a new password (>= 12 chars) twice with matching confirmation; input is not echoed.
func promptNewPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Reject non-TTY environments: prevent accidental pipe reads or scripted plaintext injection.
		return "", errors.New("reset-admin-password requires an interactive terminal")
	}
	fmt.Fprintln(os.Stdout, "Superuser admin password reset: enter new password (hidden input, at least 12 characters)")
	first, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stdout, "Re-enter new password to confirm")
	second, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	if len(first) < 12 {
		return "", errors.New("password must be at least 12 characters")
	}
	// Length policy satisfied; prompt before continuing without echoing content.
	return strings.TrimSpace(string(first)), nil
}

// promptForReset detects --reset-admin-password when passed as a global flag.
func promptForReset(args []string) bool {
	for _, a := range args {
		if a == "--reset-admin-password" || a == "-reset-admin-password" {
			return true
		}
	}
	return false
}

// flagArgsAfterReset extracts trailing --config arguments from legacy global-flag invocations.
func flagArgsAfterReset(args []string) []string {
	out := []string{}
	for _, a := range args {
		if a == "--reset-admin-password" || a == "-reset-admin-password" {
			continue
		}
		out = append(out, a)
	}
	return out
}
