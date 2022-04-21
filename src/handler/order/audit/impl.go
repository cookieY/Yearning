package audit

import (
	"Yearning-go/src/engine"
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/handler/manage/tpl"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ORDER_AGREE_MESSAGE     = "审核通过,并已转交至 %s"
	ORDER_REJECT_MESSAGE    = "已驳回"
	ORDER_AGREE_STATE       = "工单已转交！"
	ORDER_REJECT_STATE      = "工单已驳回！"
	ORDER_KILL_STATE        = "延时工单已终止！"
	ORDER_EXECUTE_STATE     = "审核通过并执行！"
	ORDER_DELAY_KILL_DETAIL = "kill指令已发送!将在到达执行时间时自动取消，状态已更改为执行失败！"
	ORDER_NOT_SEARCH        = "该阶段已有人操作通过/你不是该阶段审核人！操作不符合幂等性"
)

type ExecArgs struct {
	Order         *model.CoreSqlOrder
	Rules         engine.AuditRole
	IP            string
	Port          int
	Username      string
	Password      string
	Message       model.Message
	MaxAffectRows uint
}

type Confirm struct {
	WorkId   string `json:"work_id"`
	Page     int    `json:"page"`
	Flag     int    `json:"flag"`
	Text     string `json:"text"`
	Tp       string `json:"tp"`
	SourceId string `json:"source_id"`
}

func (e *Confirm) GetTPL() []tpl.Tpl {
	var s model.CoreDataSource
	var tpl []tpl.Tpl
	var flow model.CoreWorkflowTpl
	model.DB().Model(model.CoreDataSource{}).Select("flow_id").Where("source_id =?", e.SourceId).First(&s)
	model.DB().Model(model.CoreWorkflowTpl{}).Where("id =?", s.FlowID).First(&flow)
	_ = json.Unmarshal(flow.Steps, &tpl)
	return tpl
}

func ExecuteOrder(u *Confirm, user string) commom.Resp {
	var order model.CoreSqlOrder
	var source model.CoreDataSource
	model.DB().Where("work_id =?", u.WorkId).First(&order)

	if order.Status != 2 && order.Status != 5 {
		return commom.ERR_COMMON_MESSAGE(errors.New(ORDER_NOT_SEARCH))
	}
	order.Assigned = user

	model.DB().Model(model.CoreDataSource{}).Where("source_id =?", order.SourceId).First(&source)
	var isCall bool
	if client := lib.NewRpc(); client != nil {
		if err := client.Call("Engine.Exec", &ExecArgs{
			Order:    &order,
			Rules:    model.GloRole,
			IP:       source.IP,
			Port:     source.Port,
			Username: source.Username,
			Password: lib.Decrypt(source.Password),
			Message:  model.GloMessage,
		}, &isCall); err != nil {
			return commom.ERR_RPC
		}
		model.DB().Create(&model.CoreWorkflowDetail{
			WorkId:   u.WorkId,
			Username: user,
			Time:     time.Now().Format("2006-01-02 15:04"),
			Action:   ORDER_EXECUTE_STATE,
		})
		return commom.SuccessPayLoadToMessage(ORDER_EXECUTE_STATE)
	}
	return commom.ERR_RPC

}

func MultiAuditOrder(req *Confirm, user string) commom.Resp {

	if assigned, isExecute, ok := IsNotIdempotent(req, user); ok {
		if isExecute {
			return ExecuteOrder(req, user)
		}
		model.DB().Model(model.CoreSqlOrder{}).Where("work_id = ?", req.WorkId).Update(&model.CoreSqlOrder{CurrentStep: req.Flag + 1, Assigned: strings.Join(assigned, ",")})
		model.DB().Create(&model.CoreWorkflowDetail{
			WorkId:   req.WorkId,
			Username: user,
			Time:     time.Now().Format("2006-01-02 15:04"),
			Action:   fmt.Sprintf(ORDER_AGREE_MESSAGE, strings.Join(assigned, " ")),
		})
		lib.MessagePush(req.WorkId, 5, "")
		return commom.SuccessPayLoadToMessage(ORDER_AGREE_STATE)
	}
	return commom.ERR_COMMON_MESSAGE(errors.New(ORDER_NOT_SEARCH))
}

func RejectOrder(u *Confirm, user string) commom.Resp {
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.WorkId).Updates(map[string]interface{}{"status": 0})
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user,
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   ORDER_REJECT_MESSAGE,
	})
	model.DB().Create(&model.CoreOrderComment{
		WorkId:   u.WorkId,
		Username: user,
		Content:  fmt.Sprintf("驳回理由: %s", u.Text),
		Time:     time.Now().Format("2006-01-02 15:04"),
	})
	lib.MessagePush(u.WorkId, 0, u.Text)
	return commom.SuccessPayLoadToMessage(ORDER_REJECT_STATE)
}

func delayKill(workId string) string {
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", workId).Updates(map[string]interface{}{"status": 4, "execute_time": time.Now().Format("2006-01-02 15:04"), "is_kill": 1})
	return ORDER_DELAY_KILL_DETAIL
}

func IsNotIdempotent(r *Confirm, user string) ([]string, bool, bool) {
	tpl := r.GetTPL()
	if len(tpl) > r.Flag {
		pList := strings.Join(tpl[r.Flag].Auditor, ",")
		if !strings.Contains(pList, user) {
			return nil, false, false
		}
		if r.Flag+1 == len(tpl) {
			return tpl[r.Flag].Auditor, true, true
		}
		return tpl[r.Flag+1].Auditor, false, true
	}
	return nil, false, false
}
