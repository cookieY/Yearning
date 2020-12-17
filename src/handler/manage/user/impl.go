package user

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
)

const (
	ER_USER_REGUSTER           = "用户已存在请重新注册！"
	USER_REGUSTER_SUCCESS      = "注册成功！"
	USER_DELETE_SUCCESS        = "用户: %s 已删除"
	USER_EDIT_SUCCESS          = "邮箱/真实姓名修改成功！"
	USER_EDIT_PASSWORD_SUCCESS = "密码修改成功！"
	ADMIN_NOT_DELETE           = "admin用户无法被删除!"

	CommonExpr = "username,rule,id,department,real_name,email"
)

type CommonUserPost struct {
	Tp     string            `json:"tp"`
	User   model.CoreAccount `json:"user"`
	Origin string            `json:"origin"`
}

type CommonUserGet struct {
	User string `json:"user"`
	Tp   string `json:"tp"`
}

type fetchUser struct {
	Page int           ` json:"page"`
	Find commom.Search `json:"find"`
}

type warning struct {
	Source  []model.CoreWorkflowTpl `json:"source"`
	Grained []model.CoreRoleGroup   `json:"grained"`
	Up      []model.CoreGrained     `json:"up"`
}

func DelUserDepend(user string) commom.Resp {
	var flow []model.CoreWorkflowTpl
	var grained []model.CoreRoleGroup
	model.DB().Select("source").Where("JSON_SEARCH(steps, 'one', ?) IS NOT NULL", user).Find(&flow)
	model.DB().Select("name").Where("JSON_SEARCH(permissions, 'one',?,null,'$.auditor') IS NOT NULL", user).Find(&grained)
	return commom.SuccessPayload(warning{
		Source:  flow,
		Grained: grained,
	})
}

func SuperUserEdit(u *model.CoreAccount) commom.Resp {
	tx := model.DB().Begin()
	tx.Model(model.CoreAccount{}).Where("username = ?", u.Username).Updates(u)
	tx.Model(model.CoreSqlOrder{}).Where("username =?", u.Username).Update(model.CoreSqlOrder{RealName: u.RealName})
	tx.Model(model.CoreQueryOrder{}).Where("username =?", u.Username).Update(model.CoreQueryOrder{Realname: u.RealName})
	tx.Commit()
	return commom.SuccessPayLoadToMessage(USER_EDIT_SUCCESS)
}

func SuperUserRegister(u *model.CoreAccount) commom.Resp {
	var unique model.CoreAccount
	if !model.DB().Where("username = ?", u.Username).Select("username").First(&unique).RecordNotFound() {
		return commom.SuccessPayLoadToMessage(ER_USER_REGUSTER)
	}
	u.Password = lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))
	model.DB().Create(u)
	model.DB().Create(&model.CoreGrained{Username: u.Username, Group: lib.EmptyGroup()})
	return commom.SuccessPayLoadToMessage(USER_REGUSTER_SUCCESS)
}
