package autoTask

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/model"
	"github.com/google/uuid"
)

const (
	QUERY_EXPR = "`name` =? and source = ? and `table` =? and data_base =? and tp =?"
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
		task.Resp = common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.CREATE_MESSAGE_SUCCESS))
	} else {
		model.DB().Model(model.CoreAutoTask{}).Where("task_id =?", task.Task.TaskId).Updates(task.Task)
		task.Resp = common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.EDIT_MESSAGE_SUCCESS))
	}
}

func (task *fetchAutoTask) Activation() {
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", task.Task.ID).Update("status", task.Task.Status)
	task.Resp = common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.EDIT_MESSAGE_ACTIVE))
}
