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
	USER_EDIT_SUCCESS          = "用户信息修改成功！"
	USER_EDIT_PASSWORD_SUCCESS = "密码修改成功！"
	ADMIN_NOT_DELETE           = "admin用户无法被删除!"
	ADMIN_HAVE_DELETE_OTHER    = "非admin用户无法删除其他用户"
	USER_PROLICY_EDIT_SUCCESS  = "%s的权限已更新！"
	USER_CANNOT_DELETE         = "用户: %s 当前属于流程: %s 节点审核人,请在相关节点删除该用户审核人之后删除"

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

func SuperUserEdit(u *CommonUserPost) commom.Resp {
	tx := model.DB().Begin()
	tx.Model(model.CoreAccount{}).Where("username = ?", u.Username).Updates(u)
	tx.Model(model.CoreSqlOrder{}).Where("username =?", u.Username).Update(model.CoreSqlOrder{RealName: u.RealName})
	tx.Model(model.CoreQueryOrder{}).Where("username =?", u.Username).Update(model.CoreQueryOrder{RealName: u.RealName})
	tx.Commit()
	return commom.SuccessPayLoadToMessage(USER_EDIT_SUCCESS)
}

func SuperUserRegister(u *CommonUserPost) commom.Resp {
	var unique model.CoreAccount
	if !model.DB().Where("username = ?", u.Username).Select("username").First(&unique).RecordNotFound() {
		return commom.SuccessPayLoadToMessage(ER_USER_REGUSTER)
	}
	u.Password = lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))
	model.DB().Create(&u.CoreAccount)
	model.DB().Create(&model.CoreGrained{Username: u.Username, Group: lib.EmptyGroup()})
	return commom.SuccessPayLoadToMessage(USER_REGUSTER_SUCCESS)
}
