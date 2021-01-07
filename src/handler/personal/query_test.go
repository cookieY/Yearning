package personal

import (
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() {
	model.DbInit("../../../conf.toml")
}

func QueryRes(y yee.Context) (err error) {
	user := "admin"
	return FetchQueryResults(y, &user)
}

func BenchmarkFetchQueryResults(b *testing.B) {
	y := yee.C()
	y.POST("/api/v2/query", QueryRes)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v2/query", strings.NewReader(`{"sql":"select * from core_accounts","data_base":"public","source":"local"}`))
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}
