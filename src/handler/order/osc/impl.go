package osc

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
)

type OSCer interface {
	Kill() string
	Percent() map[string]int
}

type OSC struct {
	WorkId string `json:"work_id"`
}

func (o *OSC) Percent() map[string]int {
	var d model.CoreSqlOrder
	model.DB().Where("work_id =?", o.WorkId).First(&d)
	return map[string]int{"p": d.Percent, "s": d.Current}
}

func (o *OSC) Kill() string {
	lib.ExKillOsc(&pb.LibraAuditOrder{WorkId: o.WorkId})
	return "kill指令已发送!如工单最后显示为执行失败则生效!"
}

