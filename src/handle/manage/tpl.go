package manage

import (
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

type Tpl struct {
	Desc    string   `json:"desc"`
	Auditor []string `json:"auditor"`
	Type    int      `json:"type"`
}

type tplTypes struct {
	Steps    []Tpl  `json:"steps"`
	Source   string `json:"source"`
	Relevant int    `js`
}

func GeneralAllSources(c yee.Context) (err error) {
	var source []model.CoreDataSource
	model.DB().Select("source").Find(&source)
	return c.JSON(http.StatusOK, source)
}

func TplPostSourceTemplate(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var t model.CoreWorkflowTpl
	step, _ := json.Marshal(u.Steps)
	if model.DB().Where("source =?", u.Source).First(&t).RecordNotFound() {
		model.DB().Create(&model.CoreWorkflowTpl{Source: u.Source, Steps: step})
	} else {
		model.DB().Model(model.CoreWorkflowTpl{}).Where("source =?", u.Source).Update(model.CoreWorkflowTpl{Steps: step})
	}

	return c.JSON(http.StatusOK, "流程模板已更新")
}

func TplEditSourceTemplateInfo(c yee.Context) (err error) {
	u := new(tplTypes)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var t model.CoreWorkflowTpl
	model.DB().Where("source =?", u.Source).First(&t)
	return c.JSON(http.StatusOK, t)
}

func TplRestApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:  GeneralAllSources,
		Post: TplPostSourceTemplate,
		Put:  TplEditSourceTemplateInfo,
	}
}
