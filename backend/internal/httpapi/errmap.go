// errmap.go maps domain sentinel errors to business error envelopes. The transport layer only maps without altering semantics.
package httpapi

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/identity"
)

// writeBizError maps domain errors uniformly; unrecognized errors are handled as internal errors (without leaking details).
func writeBizError(c *gin.Context, err error) bool {
	var locked bizerr.AdminLockedError
	var immutable bizerr.AdminImmutableError
	var orders bizerr.ActiveOrdersError
	switch {
	case errors.As(err, &locked):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeAdminPasswordLocked, Name: "ADMIN_PASSWORD_LOCKED",
			Message: "Superuser admin is locked; run ./Yearning --reset-admin-password on the server",
		})
	case errors.As(err, &immutable):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeAdminImmutable, Name: "ADMIN_IMMUTABLE",
			Message: "Built-in admin cannot be deleted, renamed, or demoted",
		})
	case errors.As(err, &orders):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeUserHasActiveOrders, Name: "USER_HAS_ACTIVE_ORDERS",
			Message: "Cannot delete user with non-terminal submitted change orders",
			Meta:    map[string]any{"active_order_count": orders.Count},
		})
	case errors.Is(err, bizerr.ErrInvalidCredentials):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeInvalidCredentials, Name: "INVALID_CREDENTIALS",
			Message: "Invalid username or password",
		})
	case errors.Is(err, bizerr.ErrPasswordManagedByIdentitySource):
		WriteBusinessError(c, &BusinessError{
			Code:    bizerr.CodePasswordManagedByIdentitySource,
			Name:    "PASSWORD_MANAGED_BY_IDENTITY_SOURCE",
			Message: "Account is bound to an LDAP/OIDC identity provider; password is managed externally",
		})
	case errors.Is(err, identity.ErrExternalAdminForbidden):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeAdminImmutable, Name: "ADMIN_IMMUTABLE",
			Message: "External identities cannot map to, overwrite, or create the built-in admin account",
		})
	case errors.Is(err, bizerr.ErrValidationFailed):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "Invalid request parameters",
		})
	case errors.Is(err, bizerr.ErrNotFound):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeResourceNotFound, Name: "RESOURCE_NOT_FOUND",
			Message: "Resource not found",
		})
	case errors.Is(err, bizerr.ErrVersionConflict):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeVersionConflict, Name: "VERSION_CONFLICT",
			Message: "Resource version conflict; please refresh and retry",
		})
	case errors.Is(err, bizerr.ErrConcurrentModification):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeConcurrentModification, Name: "CONCURRENT_MODIFICATION",
			Message: "Concurrent modification conflict; please retry",
		})
	case errors.Is(err, bizerr.ErrAlreadyExists):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeResourceAlreadyExists, Name: "RESOURCE_ALREADY_EXISTS",
			Message: "Resource already exists",
		})
	case errors.Is(err, bizerr.ErrReferenced):
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeResourceReferenced, Name: "RESOURCE_REFERENCED",
			Message: "Resource is referenced and cannot be deleted",
		})
	default:
		return false
	}
	return true
}

// writeDomainError maps errors with a context-specific reference code (flow/datasource deletion reference).
// Uses generic 1006 when refCode is zero.
const registerRejectedMessage = "Unable to complete registration; check your input or try a different username"

func writeDomainError(c *gin.Context, err error, refCode int) bool {
	if errors.Is(err, bizerr.ErrReferenced) {
		code, name := bizerr.CodeResourceReferenced, "RESOURCE_REFERENCED"
		switch refCode {
		case bizerr.CodeFlowReferenced:
			code, name = bizerr.CodeFlowReferenced, "FLOW_REFERENCED"
		case bizerr.CodeDatasourceReferenced:
			code, name = bizerr.CodeDatasourceReferenced, "DATASOURCE_REFERENCED"
		}
		WriteBusinessError(c, &BusinessError{
			Code: code, Name: name,
			Message: "Resource is referenced and can only be disabled",
		})
		return true
	}
	if errors.Is(err, datasource.ErrTestRateLimited) {
		WriteProblem(c, 429, "connection test rate limited")
		return true
	}
	if errors.Is(err, identity.ErrRateLimited) {
		WriteProblem(c, 429, "rate limited")
		return true
	}
	if errors.Is(err, identity.ErrRegistrationDisabled) {
		WriteProblem(c, 403, "registration disabled")
		return true
	}
	if errors.Is(err, identity.ErrRegistrationRejected) {
		// SEC-0061: uniform rejection message on registration surface. 1001 is in the auth_register profile
		// allowlist; username existence must not be disclosed via 1005 or reserved-name semantics. Reserved names,
		// invalid parameters, and taken usernames share this exact envelope for anti-enumeration.
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: registerRejectedMessage,
		})
		return true
	}
	var connFailed datasource.ErrConnectionFailed
	if errors.As(err, &connFailed) || errors.Is(err, datasource.ErrUnsupportedCapability) ||
		errors.Is(err, datasource.ErrCredentialMissing) {
		// S18.76 fix (owner decision: following Provider rejection reason surfacing path): Detail = underlying root cause
		// (dial/auth/version is outside frozen matrix), meta.detail reaches inline UI, truncated by caller.
		detail := err.Error()
		if len(detail) > 300 {
			detail = detail[:300]
		}
		WriteBusinessError(c, &BusinessError{
			Code: 3010, Name: "DATASOURCE_UNAVAILABLE",
			Message: "Datasource connection test failed",
			Meta:    map[string]any{"detail": detail},
		})
		return true
	}
	return writeBizError(c, err)
}
