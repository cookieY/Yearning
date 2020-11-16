package post

import (
	"Yearning-go/src/handle/order"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"github.com/cookieY/yee"
	"time"
)

func CallAutoTask(u *model.CoreSqlOrder, w string, c yee.Context) {
	// todo 以下代码为autoTask代码
	var sor model.CoreDataSource
	model.DB().Where("source =?", u.Source).First(&sor)
	ps := lib.Decrypt(sor.Password)
	s := pb.LibraAuditOrder{
		IsAutoTask: true,
		DataBase:   u.DataBase,
		Name:       u.Source,
		Source: &pb.Source{
			Addr:     sor.IP,
			User:     sor.Username,
			Password: ps,
			Port:     int32(sor.Port),
		},
		SQL: u.SQL,
	}

	r := lib.ExAutoTask(&s)
	if r {
		// todo 调整参数
		s.IsDML = true
		s.Backup = u.Backup == 1
		s.WorkId = w
		s.Execute = true
		s.SQL = u.SQL

		// todo 开始执行
		rx := order.Review{Juno: s}
		go func() {
			t1 := lib.Time2StrDiff(u.Delay)
			if t1 > 0 {
				tick := time.NewTicker(t1)
				for {
					select {
					case <-tick.C:
						lib.ExDMLClient(&rx.Juno)
						tick.Stop()
						goto ENDCHECK
					}
				ENDCHECK:
					break
				}
			} else {
				lib.ExDMLClient(&rx.Juno)
			}

		}()
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", w).Updates(map[string]interface{}{"status": 3})
		model.DB().Create(&model.CoreWorkflowDetail{
			WorkId:   w,
			Username: u.Username,
			Rejected: "",
			Time:     time.Now().Format("2006-01-02 15:04"),
			Action:   "工单已执行(autoTask)",
		})
	}
}
