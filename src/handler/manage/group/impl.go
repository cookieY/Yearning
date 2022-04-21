package group

import (
	"Yearning-go/src/model"
)

const (
	GROUP_DELETE_SUCCESS = "权限组ID: %s 已删除"
	GROUP_CREATE_SUCCESS = "%s权限组已创建/编辑！"
	GROUP_EDIT_SUCCESS   = "%s的权限已更新！"
)

type policy struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	model.PermissionList
}

type marge struct {
	Username string `json:"username"`
	Group    string `json:"group"`
	IsShow   bool   `json:"is_show"`
}

type tree struct {
	Source   string `json:"source"`
	Children []tree `json:"children"`
}
