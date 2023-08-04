package roles

import (
	"Yearning-go/src/engine"
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
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
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_DATA_IS_EDIT)))
}

func SuperFetchRoles(c yee.Context) (err error) {
	var k model.CoreGlobalConfiguration
	model.DB().Select("audit_role").First(&k)
	return c.JSON(http.StatusOK, common.SuccessPayload(k.AuditRole))
}

func SuperRolesList(c yee.Context) (err error) {
	var rules []model.CoreRules
	model.DB().Model(model.CoreRules{}).Find(&rules)
	return c.JSON(http.StatusOK, common.SuccessPayload(rules))
}

func SuperRolesAdd(c yee.Context) (err error) {
	u := new(model.CoreRules)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	model.DB().Create(u)
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_RULE_IS_ADD)))
}

func SuperRoleDelete(c yee.Context) (err error) {
	u := new(model.CoreRules)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	model.DB().Model(model.CoreRules{}).Where("id =?", u.ID).Delete(&model.CoreRules{})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.RULE_IS_DELETE)))
}

func SuperRoleUpdate(c yee.Context) (err error) {
	u := new(model.CoreRules)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	model.DB().Model(model.CoreRules{}).Where("id =?", u.ID).Updates(&model.CoreRules{AuditRole: u.AuditRole, Desc: u.Desc})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_RULE_IS_UPDATED)))
}

func SuperRoleProfile(c yee.Context) (err error) {
	u := new(model.CoreRules)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	var rule model.CoreRules
	model.DB().Model(model.CoreRules{}).Where("id =?", u.ID).Find(&rule)
	return c.JSON(http.StatusOK, common.SuccessPayload(rule))
}
