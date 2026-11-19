// Package bizerr defines cross-domain sentinel errors and error code constants for business outcomes
// (api/contracts/error-codes.json v2). Domain packages return sentinels,
// and httpapi maps them to HTTP 200 + err_code envelopes without altering semantics.
package bizerr

import (
	"errors"
	"fmt"
)

// Error code catalog (common and B3-related segments).
const (
	CodeValidationFailed       = 1001
	CodeResourceNotFound       = 1002
	CodeVersionConflict        = 1003
	CodeConcurrentModification = 1004
	CodeResourceAlreadyExists  = 1005
	CodeResourceReferenced     = 1006
	CodeInvalidStateTransition = 1010

	CodeInvalidCredentials       = 1101
	CodeAdminPasswordLocked      = 1102
	CodeAdminImmutable           = 1103
	CodeUserHasActiveOrders      = 1104
	CodeUserReferencedByTemplate = 1105
	CodeFlowReferenced           = 1106
	CodeDatasourceReferenced     = 1107
	CodeFlowActorNodeEmpty       = 1108

	CodePasswordManagedByIdentitySource = 1109
)

// Shared sentinels. Messages are populated uniformly by httpapi per error code; sentinels carry no user-facing copy.
var (
	ErrValidationFailed = errors.New("bizerr: validation failed")
	ErrNotFound         = errors.New("bizerr: resource not found")
	ErrVersionConflict  = errors.New("bizerr: version conflict")
	// ErrConcurrentModification is used by operations with a C1004-only profile:
	// operation-error-profiles declares only CONCURRENT_MODIFICATION (and not VERSION_CONFLICT)
	// for delete/submit/decide/lifecycle/execution/query commands
	// (allowed_errors_are_exhaustive), so If-Match mismatches must surface as 1004.
	ErrConcurrentModification = errors.New("bizerr: concurrent modification")
	ErrAlreadyExists          = errors.New("bizerr: resource already exists")
	ErrReferenced             = errors.New("bizerr: resource referenced")
	ErrStateTransition        = errors.New("bizerr: invalid state transition")
)

// AdminLockedError indicates the built-in admin account is locked; carries fixed recovery guidance for operators.
type AdminLockedError struct{}

func (AdminLockedError) Error() string { return "bizerr: admin password locked" }

// AdminImmutableError indicates an attempt to delete, demote, or impersonate the sole built-in admin.
type AdminImmutableError struct{}

func (AdminImmutableError) Error() string { return "bizerr: builtin admin immutable" }

// ActiveOrdersError indicates the user is still the submitter of non-terminal change orders.
type ActiveOrdersError struct{ Count int64 }

func (e ActiveOrdersError) Error() string {
	return fmt.Sprintf("bizerr: user has %d active orders", e.Count)
}

// VersionedError attaches the expected optimistic-lock version.
type VersionedError struct {
	Err     error
	Version int64
}

func (e *VersionedError) Error() string { return e.Err.Error() }
func (e *VersionedError) Unwrap() error { return e.Err }

// ErrInvalidCredentials indicates invalid login credentials (uniform message without leaking account existence).
var ErrInvalidCredentials = errors.New("bizerr: invalid credentials")

// ErrPasswordManagedByIdentitySource indicates the account is bound to an LDAP/OIDC identity source,
// so its password cannot be changed via self-service in this system (dedicated to the self-service password endpoint).
var ErrPasswordManagedByIdentitySource = errors.New(
	"bizerr: password managed by identity source")
