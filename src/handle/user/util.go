package user

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
)

type account struct {
	Name  string
	Error error
}

type warning struct {
	Source  []model.CoreWorkflowTpl `json:"source"`
	Grained []model.CoreRoleGroup     `json:"grained"`
	Up      []model.CoreGrained   `json:"up"`
}

func (a *account) CleanGroup() *account {
	var ruleGroup []model.CoreRoleGroup
	model.DB().Find(&ruleGroup)
	for _, jk := range ruleGroup {
		var m1 model.PermissionList
		_ = json.Unmarshal(jk.Permissions, &m1)
		m1.Auditor = lib.ResearchDel(m1.Auditor, a.Name)
		reverse, _ := json.Marshal(m1)
		model.DB().Model(model.CoreRoleGroup{}).Where("id =?", jk.ID).Update(&model.CoreRoleGroup{Permissions: reverse})
	}
	return a
}

func (a *account) CleanAccount() *account {
	tx := model.DB().Begin()

	if err := tx.Where("username =?", a.Name).Delete(&model.CoreAccount{}).Error; err != nil {
		tx.Rollback()
		a.Error = err
		return a
	}

	if err := tx.Where("username =?", a.Name).Delete(&model.CoreGrained{}).Error; err != nil {
		tx.Rollback()
		a.Error = err
		return a
	}

	tx.Commit()
	return a
}

func DelUserDepend(user string) warning {
	var flow []model.CoreWorkflowTpl
	var grained []model.CoreRoleGroup
	model.DB().Select("source").Where("JSON_SEARCH(steps, 'one', ?) IS NOT NULL", user).Find(&flow)
	model.DB().Select("name").Where("JSON_SEARCH(permissions, 'one',?,null,'$.auditor') IS NOT NULL", user).Find(&grained)
	return warning{
		Source:  flow,
		Grained: grained,
	}
}
