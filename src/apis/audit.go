package apis

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"time"
)

type OSC struct {
	WorkId string `json:"work_id"`
}

type Review struct {
	WorkId string `json:"work_id"`
	Type   int    `json:"type"`
	Juno   pb.LibraAuditOrder
	Order  model.CoreSqlOrder
}

type Reviewer interface {
	DelayKill() string
	IsKill() bool
}

type OSCer interface {
	Kill() string
	Percent() map[string]int
}

func (r *Review) DelayKill() string {
	model.DB().Model(model.CoreSqlOrder{}).Where("work_id =?", r).Update(&model.CoreSqlOrder{IsKill: 1})
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", r).Updates(map[string]interface{}{"status": 4, "execute_time": time.Now().Format("2006-01-02 15:04")})
	return "kill指令已发送!将在到达执行时间时自动取消，状态已更改为执行失败！"
}

func (r *Review) IsKill() bool {
	var c model.CoreSqlOrder
	model.DB().Where("work_id =?", r.WorkId).First(&c)
	return c.IsKill == 1
}

func (r *Review) Init() *Review {
	var sor model.CoreDataSource
	model.DB().Where("source =?", r.Order.Source).First(&sor)

	ps := lib.Decrypt(sor.Password)
	r.Juno = pb.LibraAuditOrder{
		SQL:      r.Order.SQL,
		Backup:   r.Order.Backup == 1,
		Execute:  true,
		Source:   &pb.Source{Addr: sor.IP, Port: int32(sor.Port), User: sor.Username, Password: ps},
		WorkId:   r.Order.WorkId,
		IsDML:    false,
		DataBase: r.Order.DataBase,
		Table:    r.Order.Table,
	}
	return r
}

func (r *Review) Executor() {
	switch r.Order.Type {
	case 0:
		go func() {
			t1 := lib.TimerEx(&r.Order)
			if t1 > 0 {
				tick := time.NewTicker(t1)
				for {
					select {
					case <-tick.C:
						lib.ExDDLClient(&r.Juno)
						tick.Stop()
						goto ENDCHECK
					}
				ENDCHECK:
					break
				}
			} else {
				lib.ExDDLClient(&r.Juno)
			}
		}()
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", r.Order.WorkId).Updates(map[string]interface{}{"status": 3})

	case 1:
		go func() {
			r.Juno.IsDML = true
			t1 := lib.TimerEx(&r.Order)
			if t1 > 0 {
				tick := time.NewTicker(t1)
				for {
					select {
					case <-tick.C:
						lib.ExDMLClient(&r.Juno)
						tick.Stop()
						goto ENDCHECK
					}
				ENDCHECK:
					break
				}
			} else {
				lib.ExDMLClient(&r.Juno)
			}
		}()
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", r.Order.WorkId).Updates(map[string]interface{}{"status": 3})
	}
}

func (o *OSC) Percent() map[string]int {
	var d model.CoreSqlOrder
	model.DB().Where("work_id =?", o.WorkId).First(&d)
	return map[string]int{"p": d.Percent, "s": d.Current}
}

func (o *OSC) Kill() string {
	lib.ExKillOsc(&pb.LibraAuditOrder{WorkId:o.WorkId})
	return "kill指令已发送!如工单最后显示为执行失败则生效!"
}
