package tpl

import (
	"Yearning-go/src/i18n"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
)

type Tpl struct {
	Desc    string   `json:"desc"`
	Auditor []string `json:"auditor"`
	Type    int      `json:"type"`
}

type tplTypes struct {
	Steps    []Tpl  `json:"steps"`
	Source   string `json:"source"`
	ID       int    `json:"id"`
	Relevant int    `json:"relevant"`
}

type ReqTpl struct {
	Source string `json:"source"`
	Page   int    ` json:"page"`
}

func OrderRelation(source_id string) ([]Tpl, error) {
	var s model.CoreDataSource
	var tpl model.CoreWorkflowTpl
	var whoIsAuditor []Tpl
	model.DB().Model(model.CoreDataSource{}).Where("source_id = ?", source_id).First(&s)
	if err := model.DB().Model(model.CoreWorkflowTpl{}).Where("id =?", s.FlowID).First(&tpl).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New(i18n.DefaultLang.Load(i18n.ER_MISSING_DATA_SOURCE))
	}
	_ = json.Unmarshal(tpl.Steps, &whoIsAuditor)

	return whoIsAuditor, nil
}
