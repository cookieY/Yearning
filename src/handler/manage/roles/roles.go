package roles

import (
	"Yearning-go/src/engine"
	"Yearning-go/src/handler/common"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperSaveRoles(c yee.Context) (err error) {

	u := new(engine.AuditRole)

	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	audit, _ := json.Marshal(u)
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("1=1").Updates(&model.CoreGlobalConfiguration{AuditRole: audit})
	model.GloRole = *u
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.DATA_IS_EDIT))
}

func SuperFetchRoles(c yee.Context) (err error) {
	var k model.CoreGlobalConfiguration
	model.DB().Select("audit_role").First(&k)
	return c.JSON(http.StatusOK, common.SuccessPayload(k.AuditRole))
}
