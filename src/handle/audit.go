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
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/pingcap/parser"
	"net/http"
	"time"
)

type fetchorder struct {
	Picker []string
	User   string
	Valve  bool
	Text   string
}

type f struct {
	Page int
	Find fetchorder
}

type reject struct {
	Text string
	Work string
}

type executeStr struct {
	WorkId  string
	Perform string
	Page    int
}

type referorder struct {
	Data model.CoreSqlOrder
}

func FetchAuditOrder(c echo.Context) (err error) {
	u := new(f)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	user, rule := lib.JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	queryField := "work_id, username, text, backup, date, real_name, executor, `status`, `type`, `delay`"

	whereField := "%s = ? AND username LIKE ?"

	dateField := " AND date >= ? AND date <= ?"

	start, end := lib.Paging(u.Page, 20)

	if rule == "perform" {
		if u.Find.Valve {
			whereField = fmt.Sprintf(whereField, "executor")
			if u.Find.Picker[0] == "" {
				model.DB().Select(queryField).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Order("id desc").Offset(start).Limit(end).Find(&order)
				model.DB().Model(&model.CoreSqlOrder{}).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Count(&pg)
			} else {
				model.DB().Select(queryField).Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Order("id desc").Offset(start).Limit(end).Find(&order)
				model.DB().Model(&model.CoreSqlOrder{}).Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg)
			}
		} else {
			model.DB().Select(queryField).Where("executor = ?", user).Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreSqlOrder{}).Where("executor = ?", user).Count(&pg)
		}
	} else {
		if u.Find.Valve {
			whereField = fmt.Sprintf(whereField, "assigned")
			if u.Find.Picker[0] == "" {
				model.DB().Select(queryField).
					Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Order("id desc").Offset(start).Limit(end).Find(&order)
				model.DB().Model(&model.CoreSqlOrder{}).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Count(&pg)
			} else {
				model.DB().Select(queryField).
					Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Order("id desc").Offset(start).Limit(end).Find(&order)
				model.DB().Model(&model.CoreSqlOrder{}).Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg)
			}
		} else {
			model.DB().Select(queryField).Where("assigned = ?", user).Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreSqlOrder{}).Where("assigned = ?", user).Count(&pg)
		}
	}

	var ex []model.CoreAccount

	model.DB().Where("rule ='perform'").Find(&ex)

	call := struct {
		Multi    bool                 `json:"multi"`
		Data     []model.CoreSqlOrder `json:"data"`
		Page     int                  `json:"page"`
		Executor []model.CoreAccount  `json:"multi_list"`
	}{
		model.GloOther.Multi,
		order,
		pg,
		ex,
	}
	return c.JSON(http.StatusOK, call)
}

func FetchOrderSQL(c echo.Context) (err error) {
	u := c.QueryParam("k")
	var sql model.CoreSqlOrder
	var s []map[string]string
	model.DB().Select(" `sql`, `delay`, `id_c`, `source`,`data_base`,`table`, `text`, `type`").Where("work_id =?", u).First(&sql)
	sqlParser := parser.New()
	stmtNodes, _, err := sqlParser.Parse(sql.SQL, "", "")
	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, struct {
			Delay  string              `json:"delay"`
			SQL    []map[string]string `json:"sql"`
			IDC    string              `json:"idc"`
			Source string              `json:"source"`
			Base   string              `json:"base"`
			Table  string              `json:"table"`
			Text   string              `json:"text"`
			Type   uint                `json:"type"`
		}{
			sql.Delay,
			[]map[string]string{{"SQL": sql.SQL}},
			sql.IDC,
			sql.Source,
			sql.DataBase,
			sql.Table,
			sql.Text,
			sql.Type,
		})
	}
	for _, i := range stmtNodes {
		s = append(s, map[string]string{"SQL": i.Text()})
	}
	return c.JSON(http.StatusOK, struct {
		Delay  string              `json:"delay"`
		SQL    []map[string]string `json:"sql"`
		IDC    string              `json:"idc"`
		Source string              `json:"source"`
		Base   string              `json:"base"`
		Table  string              `json:"table"`
		Text   string              `json:"text"`
		Type   uint                `json:"type"`
	}{
		sql.Delay,
		s,
		sql.IDC,
		sql.Source,
		sql.DataBase,
		sql.Table,
		sql.Text,
		sql.Type,
	})
}

func RejectOrder(c echo.Context) (err error) {
	u := new(reject)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.Work).Updates(map[string]interface{}{"rejected": u.Text, "status": 0})
	lib.MessagePush(c, u.Work, 0, u.Text)
	return c.JSON(http.StatusOK, "工单已驳回！")
}

func ExecuteOrder(c echo.Context) (err error) {
	u := new(executeStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	var order model.CoreSqlOrder
	var sor model.CoreDataSource
	var backup bool
	model.DB().Where("work_id =?", u.WorkId).First(&order)
	model.DB().Where("source =?", order.Source).First(&sor)

	if order.Backup == 1 {
		backup = true
	}

	ps := lib.Decrypt(sor.Password)

	s := pb.LibraAuditOrder{
		SQL:      order.SQL,
		Backup:   backup,
		Execute:  true,
		Source:   &pb.Source{Addr: sor.IP, Port: int32(sor.Port), User: sor.Username, Password: ps},
		WorkId:   order.WorkId,
		IsDML:    false,
		DataBase: order.DataBase,
		Table:    order.Table,
	}

	if order.Status != 2 && order.Status != 5 {
		c.Logger().Error("工单已执行过！操作不符合幂等性")
		return c.JSON(http.StatusOK, "工单已执行过！操作不符合幂等性")
	}

	if order.Type == 0 {
		go func() {
			t1 := lib.TimerEx(&order)
			if t1 > 0 {
				tick := time.NewTicker(t1)
				for {
					select {
					case <-tick.C:
						lib.ExDDLClient(&s)
						tick.Stop()
						goto ENDCHECK
					}
				ENDCHECK:
					break
				}
			} else {
				lib.ExDDLClient(&s)
			}
		}()
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", order.WorkId).Updates(map[string]interface{}{"status": 3})
	}

	if order.Type == 1 {
		s.IsDML = true
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
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", order.WorkId).Updates(map[string]interface{}{"status": 3})
	}
	return c.JSON(http.StatusOK, "工单已执行！")
}

func RollBackSQLOrder(c echo.Context) (err error) {
	u := new(referorder)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	w := lib.GenWorkid()
	model.DB().Create(&model.CoreSqlOrder{
		WorkId:   w,
		Username: u.Data.Username,
		Status:   2,
		Type:     u.Data.Type,
		Backup:   u.Data.Backup,
		IDC:      u.Data.IDC,
		Source:   u.Data.Source,
		DataBase: u.Data.DataBase,
		Table:    u.Data.Table,
		Date:     time.Now().Format("2006-01-02 15:04"),
		SQL:      u.Data.SQL,
		Text:     u.Data.Text,
		Assigned: u.Data.Assigned,
		Delay:    u.Data.Delay,
		RealName: u.Data.RealName,
		Time:     time.Now().Format("2006-01-02"),
	})
	lib.MessagePush(c, w, 2, "")
	return c.JSON(http.StatusOK, "工单已提交,请等待审核人审核！")
}

type ber struct {
	U []string
}

func UndoAuditOrder(c echo.Context) (err error) {
	u := new(ber)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	tx := model.DB().Begin()
	for _, i := range u.U {
		tx.Where("work_id =?", i).Delete(&model.CoreSqlOrder{})
		tx.Where("work_id =?", i).Delete(&model.CoreRollback{})
		tx.Where("work_id =?", i).Delete(&model.CoreSqlRecord{})
	}
	tx.Commit()
	return c.JSON(http.StatusOK, "工单已删除！")
}

func MulitAuditOrder(c echo.Context) (err error) {
	req := new(executeStr)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", req.WorkId).Update(&model.CoreSqlOrder{Executor: req.Perform, Status: 5})
	lib.MessagePush(c, req.WorkId, 5, "")
	return c.JSON(200, "工单已提交执行人！")

}

func OscPercent(c echo.Context) (err error) {
	r := c.Param("work_id")
	var d model.CoreSqlOrder
	model.DB().Where("work_id =?", r).First(&d)
	return c.JSON(http.StatusOK, map[string]int{"p": d.Percent, "s": d.Current})
}

func OscKill(c echo.Context) (err error) {
	r := c.Param("work_id")
	lib.ExKillOsc(&pb.LibraAuditOrder{WorkId:r})
	return c.JSON(http.StatusOK, "kill指令已发送!如工单最后显示为执行失败则生效!")
}

func DelayKill(c echo.Context) (err error) {
	r := c.Param("work_id")
	model.DB().Model(model.CoreSqlOrder{}).Where("work_id =?", r).Update(&model.CoreSqlOrder{IsKill: 1})
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", r).Updates(map[string]interface{}{"status": 4, "execute_time": time.Now().Format("2006-01-02 15:04")})
	return c.JSON(http.StatusOK, "kill指令已发送!将在到达执行时间时自动取消，状态已更改为执行失败！")
}