// Copyright 2019 HenryYee.
//
// Licensed under the AGPL, Version 3.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.gnu.org/licenses/agpl-3.0.en.html
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package handle

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"github.com/labstack/echo/v4"
	"net/http"
	"time"
)

type ddl struct {
	Source   string
	SQL      string
	Database string
	Table    string
	IDC      string
	Text     string
	Assigned string
	Delay    string
	Backup   uint
	IsDML    bool
}

type ddlrefer struct {
	DDL ddl
	SQL string
	Ty  uint
}

func SQLReferToOrder(c echo.Context) (err error) {
	u := new(ddlrefer)
	user, _ := lib.JwtParse(c)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}

	var account model.CoreAccount
	model.DB().Select("real_name").Where("username =?", user).First(&account)
	w := lib.GenWorkid()
	model.DB().Create(&model.CoreSqlOrder{
		WorkId:   w,
		Username: user,
		Status:   2,
		Type:     u.Ty,
		Backup:   u.DDL.Backup,
		IDC:      u.DDL.IDC,
		Source:   u.DDL.Source,
		DataBase: u.DDL.Database,
		Table:    u.DDL.Table,
		Date:     time.Now().Format("2006-01-02 15:04"),
		SQL:      u.SQL,
		Text:     u.DDL.Text,
		Assigned: u.DDL.Assigned,
		Delay:    u.DDL.Delay,
		RealName: account.RealName,
		Time:     time.Now().Format("2006-01-02"),
	})
	lib.MessagePush(c, w, 2, "")

	// todo 以下代码为autoTask代码
	var sor model.CoreDataSource
	model.DB().Where("source =?", u.DDL.Source).First(&sor)
	ps := lib.Decrypt(sor.Password)
	s := pb.LibraAuditOrder{
		IsAutoTask: true,
		DataBase:   u.DDL.Database,
		Name:       u.DDL.Source,
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
		var order model.CoreSqlOrder
		model.DB().Where("work_id =?", w).First(&order)
		backup := false
		if u.DDL.Backup == 1 {
			backup = true
		}
		// todo 调整参数
		s.IsDML = true
		s.WorkId = w
		s.Backup = backup
		s.Execute = true
		s.SQL = u.SQL

		// todo 开始执行

		go func() {
			t1 := lib.TimerEx(&order)
			if t1 > 0 {
				tick := time.NewTicker(t1)
				for {
					select {
					case <-tick.C:
						lib.ExDMLClient(&s)
						tick.Stop()
						goto ENDCHECK
					}
				ENDCHECK:
					break
				}
			} else {
				lib.ExDMLClient(&s)
			}

		}()
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", w).Updates(map[string]interface{}{"status": 3})
	}

	return c.JSON(http.StatusOK, "工单已提交,请等待审核人审核！")
}
