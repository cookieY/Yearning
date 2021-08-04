package roles

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	ser "Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperSaveRoles(c yee.Context) (err error) {

	u := new(ser.AuditRole)
	
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	ser.FetchAuditRole = *u
	audit, _ := json.Marshal(u)
	model.DB().Model(model.CoreGlobalConfiguration{}).Updates(&model.CoreGlobalConfiguration{AuditRole: audit})
	lib.OverrideConfig(&pb.LibraAuditOrder{})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_EDIT))
}

func SuperFetchRoles(c yee.Context) (err error) {
	var k model.CoreGlobalConfiguration
	model.DB().Select("audit_role").First(&k)
	return c.JSON(http.StatusOK, commom.SuccessPayload(k))
}
