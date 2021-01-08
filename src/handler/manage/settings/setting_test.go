package settings

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/model"
	"Yearning-go/src/test"
	"github.com/stretchr/testify/assert"
	"net/http"
	"os"
	"testing"
)

func setup() {
	model.DbInit("../../../../conf.toml")
	apis.NewTest()
}

var apis = test.Case{
	Method:  http.MethodPost,
	Uri:     "/api/v2/manage/setting",
	Handler: SettingsApis(),
}

func TestMain(m *testing.M) {
	setup()
	code := m.Run()
	os.Exit(code)
}

func TestSuperFetchSetting(t *testing.T) {
	var Ref commom.Resp
	apis.Get("").Do().Unmarshal(&Ref)
	assert.NotNil(t, len(Ref.Payload.(map[string]interface{})))
	assert.Equal(t, 1200, Ref.Code)
}