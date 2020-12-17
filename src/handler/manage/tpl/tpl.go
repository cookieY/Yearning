package tpl

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

func GeneralAllSources(c yee.Context) (err error) {
	var source []model.CoreDataSource
	model.DB().Select("source").Find(&source)
	return c.JSON(http.StatusOK, commom.SuccessPayload(source))
}

func TplPostSourceTemplate(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var t model.CoreWorkflowTpl
	step, _ := json.Marshal(u.Steps)
	if model.DB().Where("source =?", u.Source).First(&t).RecordNotFound() {
		model.DB().Create(&model.CoreWorkflowTpl{Source: u.Source, Steps: step})
	} else {
		model.DB().Model(model.CoreWorkflowTpl{}).Where("source =?", u.Source).Update(model.CoreWorkflowTpl{Steps: step})
	}

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_UPDATED))
}

func TplEditSourceTemplateInfo(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var t model.CoreWorkflowTpl
	model.DB().Where("source =?", u.Source).First(&t)
	return c.JSON(http.StatusOK, commom.SuccessPayload(t))
}
