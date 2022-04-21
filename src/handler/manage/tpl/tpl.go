package tpl

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

func TplGetAPis(c yee.Context) (err error) {
	switch c.QueryParam("tp") {
	case "user":
		var user []model.CoreAccount
		model.DB().Select("username,real_name").Find(&user)
		return c.JSON(http.StatusOK, commom.SuccessPayload(user))
	case "flow":
		var flows []model.CoreWorkflowTpl
		model.DB().Model(model.CoreWorkflowTpl{}).Find(&flows)
		return c.JSON(http.StatusOK, commom.SuccessPayload(flows))
	default:
		return
	}
}

func TplPostSourceTemplate(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var t model.CoreWorkflowTpl
	step, _ := json.Marshal(u.Steps)
	if model.DB().Where("id =?", u.ID).First(&t).RecordNotFound() {
		model.DB().Create(&model.CoreWorkflowTpl{Source: u.Source, Steps: step})
	} else {
		model.DB().Model(model.CoreWorkflowTpl{}).Where("id =?", u.ID).Update(model.CoreWorkflowTpl{Source: u.Source, Steps: step})
	}

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_UPDATED))
}

func EditSourceTemplateInfo(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var t model.CoreWorkflowTpl
	model.DB().Where("id =?", u.ID).First(&t)
	return c.JSON(http.StatusOK, commom.SuccessPayload(t))
}

func DeleteSourceTemplateInfo(c yee.Context) (err error) {
	id := c.QueryParam("id")
	model.DB().Model(model.CoreWorkflowTpl{}).Where("id =?", id).Delete(&model.CoreWorkflowTpl{})
	model.DB().Model(model.CoreDataSource{}).Where("flow_id =?", id).Updates(&model.CoreDataSource{FlowID: -1})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_DELETE))
}
