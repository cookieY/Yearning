package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/buildinfo"
)

// HandleVersion returns build metadata (SEC-0044): anonymous requests return only service name and API
// version without disclosing commit/Go version fingerprints; authenticated sessions see the full summary.
// Version facts come exclusively from build-time injection without guessing release state.
func HandleVersion(c *gin.Context) {
	data := gin.H{"service": "yearning", "api_version": "v4"}
	if session.FromContext(c.Request.Context()) != nil {
		for k, v := range buildinfo.Summary() {
			data[k] = v
		}
	}
	WriteSuccess(c, data)
}
