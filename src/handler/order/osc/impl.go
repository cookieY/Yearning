package osc

type OSCer interface {
	//Kill() string
	Percent() map[string]int
}

type OSC struct {
	WorkId string `json:"work_id"`
}

//func (o *OSC) Percent() map[string]int {
//	var d model.CoreSqlOrder
//	model.DB().Where("work_id =?", o.WorkId).First(&d)
//	return map[string]int{"p": d.Percent, "s": d.Current}
//}

//func (o *OSC) Kill() string {
//	ser.OscIsKill[o.WorkId] = true
//	return "kill指令已发送!如工单最后显示为执行失败则生效!"
//}
