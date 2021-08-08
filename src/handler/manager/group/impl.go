package group

import "Yearning-go/src/model"

const (
	GROUP_DELETE_SUCCESS = "权限组: %s 已删除"
	GROUP_CREATE_SUCCESS  = "%s权限组已创建/编辑！"
	GROUP_EDIT_SUCCESS = "%s的权限已更新！"
)

type k struct {
	Username   string
	Permission model.PermissionList
	Tp         int
	Group      []string
}

type marge struct {
	Username   string `json:"username"`
	Group  string `json:"group"`
	IsShow bool   `json:"is_show"`
}