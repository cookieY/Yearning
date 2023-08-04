package group

import (
	"Yearning-go/src/model"
)

type policy struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	model.PermissionList
}

type tree struct {
	Source   string `json:"source"`
	Children []tree `json:"children"`
}
