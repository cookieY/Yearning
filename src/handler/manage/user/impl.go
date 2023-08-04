package user

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"errors"
	"gorm.io/gorm"
)

const (
	CommonExpr = "username,id,department,real_name,email,is_recorder"
)

type CommonUserPost struct {
	model.CoreAccount
	Origin string   `json:"origin"`
	Group  []string `json:"group"`
}

type CommonUserGet struct {
	User string `json:"user"`
	Tp   string `json:"tp"`
}

type warning struct {
	Source  []model.CoreWorkflowTpl `json:"source"`
	Grained []model.CoreRoleGroup   `json:"grained"`
	Up      []model.CoreGrained     `json:"up"`
}

func DelUserDepend(user string) common.Resp {
	var flow []model.CoreWorkflowTpl
	var grained []model.CoreRoleGroup
	model.DB().Select("source").Where("JSON_SEARCH(steps, 'one', ?) IS NOT NULL", user).Find(&flow)
	model.DB().Select("name").Where("JSON_SEARCH(permissions, 'one',?,null,'$.auditor') IS NOT NULL", user).Find(&grained)
	return common.SuccessPayload(warning{
		Source:  flow,
		Grained: grained,
	})
}

func SuperUserEdit(u *CommonUserPost) common.Resp {
	tx := model.DB().Begin()
	tx.Model(model.CoreAccount{}).Where("username = ?", u.Username).Updates(map[string]interface{}{
		"department":  u.Department,
		"real_name":   u.RealName,
		"email":       u.Email,
		"is_recorder": u.IsRecorder,
	})
	tx.Model(model.CoreSqlOrder{}).Where("username =?", u.Username).Updates(model.CoreSqlOrder{RealName: u.RealName})
	tx.Model(model.CoreQueryOrder{}).Where("username =?", u.Username).Updates(model.CoreQueryOrder{RealName: u.RealName})
	tx.Commit()
	return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.USER_EDIT_SUCCESS))
}

func SuperUserRegister(u *CommonUserPost) common.Resp {
	var unique model.CoreAccount
	if err := model.DB().Where("username = ?", u.Username).Select("username").First(&unique).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.ER_USER_REGUSTER))
	}
	u.Password = lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))
	model.DB().Create(&u.CoreAccount)
	model.DB().Create(&model.CoreGrained{Username: u.Username, Group: lib.EmptyGroup()})
	return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.USER_REGUSTER_SUCCESS))
}
