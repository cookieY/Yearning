package audit

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"time"
)

const (
	UPDATE_RAW_SQL = "update core_sql_orders set relevant = JSON_ARRAY_APPEND(relevant, '$', ?), assigned = ? , current_step = ? where work_id =?"

	ORDER_AGREE_MESSAGE  = "审核通过,并已转交至%s"
	ORDER_REJECT_MESSAGE = "驳回"
	ORDER_AGREE_STATE    = "工单已转交！"
	ORDER_REJECT_STATE   = "工单已驳回！"
	ORDER_KILL_STATE     = "延时工单已终止！"
	ORDER_EXECUTE_STATE  = "审核通过并执行！"
	ORDER_DELAY_KILL_DETAIL = "kill指令已发送!将在到达执行时间时自动取消，状态已更改为执行失败！"
	IDEMPOTENT = "工单已执行过！操作不符合幂等性"
)

func MultiAuditOrder(req *commom.ExecuteStr, user string) commom.Resp {
	model.DB().Exec(UPDATE_RAW_SQL, req.Perform, req.Perform, req.Flag+1, req.WorkId)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   req.WorkId,
		Username: user,
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   fmt.Sprintf(ORDER_AGREE_MESSAGE, req.Perform),
	})
	lib.MessagePush(req.WorkId, 5, "")
	return commom.SuccessPayLoadToMessage(ORDER_AGREE_STATE)
}

func RejectOrder(u *commom.ExecuteStr, user string) commom.Resp {
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.WorkId).Updates(map[string]interface{}{"status": 0})
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user,
		Rejected: u.Text,
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   ORDER_REJECT_MESSAGE,
	})
	lib.MessagePush(u.WorkId, 0, u.Text)
	return commom.SuccessPayLoadToMessage(ORDER_REJECT_STATE)
}

func delayKill(workId string) string {
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", workId).Updates(map[string]interface{}{"status": 4, "execute_time": time.Now().Format("2006-01-02 15:04"), "is_kill": 1})
	return ORDER_DELAY_KILL_DETAIL
}
