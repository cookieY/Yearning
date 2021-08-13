package autoTask

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/model"
)

const (
	CREATE_MESSAGE_SUCCESS = "已添加autoTask任务!"
	CREATE_MESSAGE_ERROR   = "请勿重复添加相同名称的任务!"
	EDIT_MESSAGE_SUCCESS   = "AutoTask信息已变更！"
	EDIT_MESSAGE_ACTIVE    = "AutoTask工单状态已变更！"
	QUERY_EXPR             = "`name` =? and source = ? and `table` =? and data_base =? and tp =?"
)

type fetchAutoTask struct {
	Task model.CoreAutoTask `json:"task"`
	Tp   string             `json:"tp"`
	Page int                ` json:"page"`
	Find commom.Search      `json:"find"`
	Resp commom.Resp        `json:"resp"`
}

func (task *fetchAutoTask) Create() {
	var tmp model.CoreAutoTask
	if model.DB().Model(model.CoreAutoTask{}).Where(
		QUERY_EXPR, task.Task.Name, task.Task.Source, task.Task.Table, task.Task.DataBase, task.Task.Tp).
		First(&tmp).RecordNotFound() {
		model.DB().Model(model.CoreAutoTask{}).Create(&task.Task)
		task.Resp = commom.SuccessPayLoadToMessage(CREATE_MESSAGE_SUCCESS)
	} else {
		task.Resp = commom.SuccessPayLoadToMessage(CREATE_MESSAGE_ERROR)
	}
}

func (task *fetchAutoTask) Edit() {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.Task.ID).Update(task.Task)
	task.Resp = commom.SuccessPayLoadToMessage(EDIT_MESSAGE_SUCCESS)
}

func (task *fetchAutoTask) Activation() {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.Task.ID).Update("status", task.Task.Status)
	task.Resp = commom.SuccessPayLoadToMessage(EDIT_MESSAGE_ACTIVE)
}
