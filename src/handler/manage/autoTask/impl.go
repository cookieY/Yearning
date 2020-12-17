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
}

func SuperCreateAutoTask(task model.CoreAutoTask) commom.Resp {
	var tmp model.CoreAutoTask
	if model.DB().Model(model.CoreAutoTask{}).Where(
		QUERY_EXPR, task.Name, task.Source, task.Table, task.DataBase, task.Tp).
		First(&tmp).RecordNotFound() {
		model.DB().Create(&task)
		return commom.SuccessPayLoadToMessage(CREATE_MESSAGE_SUCCESS)
	}
	return commom.SuccessPayLoadToMessage(CREATE_MESSAGE_ERROR)
}

func SuperEditAutoTask(task model.CoreAutoTask) commom.Resp {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.ID).Update(task)
	return commom.SuccessPayLoadToMessage(EDIT_MESSAGE_SUCCESS)
}

func SuperAutoTaskActivation(task model.CoreAutoTask) commom.Resp {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.ID).Update("status", task.Status)
	return commom.SuccessPayLoadToMessage(EDIT_MESSAGE_ACTIVE)
}
