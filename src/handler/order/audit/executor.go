package audit

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"time"
)

type Review struct {
	Juno  pb.LibraAuditOrder
	Type  uint
	Delay string
	Step  int
	User  string
}

func (r *Review) IsKill() bool {
	var c model.CoreSqlOrder
	model.DB().Where("work_id =?", r.Juno.WorkId).First(&c)
	return c.IsKill == 1
}

func (r *Review) Init(order model.CoreSqlOrder) *Review {
	var sor model.CoreDataSource
	model.DB().Select("password,username,ip,port").Where("source =?", order.Source).First(&sor)

	ps := lib.Decrypt(sor.Password)

	r.Juno = pb.LibraAuditOrder{
		SQL:      order.SQL,
		Backup:   order.Backup == 1,
		Execute:  true,
		Source:   &pb.Source{Addr: sor.IP, Port: int32(sor.Port), User: sor.Username, Password: ps},
		WorkId:   order.WorkId,
		IsDML:    false,
		DataBase: order.DataBase,
		Table:    order.Table,
	}
	r.Type = order.Type
	r.Delay = order.Delay
	r.Step = order.CurrentStep + 1
	r.User = order.Assigned
	return r
}

func (r *Review) Executor() {
	switch r.Type {
	case 0:
		go func() {
			t1 := lib.Time2StrDiff(r.Delay)
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
	case 1:
		go func() {
			r.Juno.IsDML = true
			t1 := lib.Time2StrDiff(r.Delay)
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
	}
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", r.Juno.WorkId).Updates(map[string]interface{}{"status": 3, "current_step": r.Step, "assigned": r.User})
}
