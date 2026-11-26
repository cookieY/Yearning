// paging.go shared query-parameter filter and cursor pagination envelope helpers for HTTP handlers.
package httpapi

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
)

// RCP-20261105-ADMIN-LIST-FIELD-FILTERS: per-column filter parameters for the four admin list endpoints.
// Uniform parsing rules: empty string = omitted; enums accept only contract values; booleans accept only true/false;
// UUIDs must parse cleanly. Invalid values always return 1001 without silent fallback.

func filterEnumQuery(c *gin.Context, name string, allowed ...string) (string, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return "", true
	}
	for _, candidate := range allowed {
		if raw == candidate {
			return raw, true
		}
	}
	WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
	return "", false
}

func filterBoolQuery(c *gin.Context, name string) (*bool, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	value := raw == "true"
	if !value && raw != "false" {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return nil, false
	}
	return &value, true
}

func filterUUIDQuery(c *gin.Context, name string) (string, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return "", true
	}
	if _, err := uuid.Parse(raw); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid resource identifier"})
		return "", false
	}
	return raw, true
}

func intQuery(c *gin.Context, name string, def int) int {
	raw := c.Query(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func writePage(c *gin.Context, items any, next *string) {
	var nextVal any
	if next != nil {
		nextVal = *next
	}
	WriteSuccess(c, gin.H{
		"items": items,
		"page":  gin.H{"next_cursor": nextVal, "has_more": next != nil},
	})
}
