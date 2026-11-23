// errmap_datasource_ssrf_test.go verifies that datasource create/replace SSRF rejections map to the
// declared 1001 VALIDATION_FAILED envelope carrying safe meta.detail and meta.fields.host (never
// undeclared 3010 DATASOURCE_UNAVAILABLE which triggers generic client contract-violation fallbacks).
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestDatasourceEndpointForbiddenMapsToValidationFailedWithDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	err := datasource.ErrEndpointForbidden{Err: addrguard.ErrCloudMetadataForbidden}
	if !errors.Is(err, bizerr.ErrValidationFailed) {
		t.Fatalf("ErrEndpointForbidden must unwrap to bizerr.ErrValidationFailed")
	}
	if !errors.Is(err, addrguard.ErrCloudMetadataForbidden) || !errors.Is(err, addrguard.ErrAddressForbidden) {
		t.Fatalf("ErrEndpointForbidden must unwrap to addrguard sentinels")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v4/admin/datasources", nil)

	if handled := writeDomainError(c, err, 0); !handled {
		t.Fatal("writeDomainError returned false; want true")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d; want 200", w.Code)
	}
	var env struct {
		ErrCode int            `json:"err_code"`
		Message string         `json:"message"`
		Meta    map[string]any `json:"meta"`
	}
	if uerr := json.Unmarshal(w.Body.Bytes(), &env); uerr != nil {
		t.Fatalf("unmarshal response: %v (body=%s)", uerr, w.Body.String())
	}
	if env.ErrCode != bizerr.CodeValidationFailed {
		t.Fatalf("err_code = %d; want %d (VALIDATION_FAILED)", env.ErrCode, bizerr.CodeValidationFailed)
	}
	detail, _ := env.Meta["detail"].(string)
	if !strings.Contains(detail, "cloud metadata") {
		t.Fatalf("meta.detail = %q; want cloud metadata reason", detail)
	}
	fields, _ := env.Meta["fields"].(map[string]any)
	hostMsgs, _ := fields["host"].([]any)
	if len(hostMsgs) == 0 {
		t.Fatalf("meta.fields.host = %v; want non-empty host field error", fields)
	}
}
