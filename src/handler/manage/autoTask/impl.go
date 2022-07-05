package autoTask

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/model"
	"github.com/google/uuid"
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
	Find common.Search      `json:"find"`
	Resp common.Resp        `json:"resp"`
}

func (task *fetchAutoTask) CURD() {
	if task.Task.TaskId == "" {
		task.Task.TaskId = uuid.New().String()
		model.DB().Model(model.CoreAutoTask{}).Create(&task.Task)
		task.Resp = common.SuccessPayLoadToMessage(CREATE_MESSAGE_SUCCESS)
	} else {
		model.DB().Model(model.CoreAutoTask{}).Where("task_id =?", task.Task.TaskId).Updates(task.Task)
		task.Resp = common.SuccessPayLoadToMessage(EDIT_MESSAGE_SUCCESS)
	}
}

func (task *fetchAutoTask) Activation() {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.Task.ID).Update("status", task.Task.Status)
	task.Resp = common.SuccessPayLoadToMessage(EDIT_MESSAGE_ACTIVE)
}
