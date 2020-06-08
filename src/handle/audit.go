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
	"Yearning-go/src/apis"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/cookieY/yee"
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

type singleOrder struct {
	model.CoreSqlOrder
	SQLS []map[string]string `json:"sqls"`
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

func FetchAuditOrder(c yee.Context) (err error) {
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
				model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
			} else {
				model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
			}
		} else {
			model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).Where("executor = ?", user).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
		}
	} else {
		if u.Find.Valve {
			whereField = fmt.Sprintf(whereField, "assigned")
			if u.Find.Picker[0] == "" {
				model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).
					Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
			} else {
				model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).
					Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
			}
		} else {
			model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).Where("assigned = ?", user).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
		}
	}

	var ex []model.CoreAccount

	model.DB().Where("rule ='perform'").Find(&ex)

	return c.JSON(http.StatusOK, map[string]interface{}{"multi": model.GloOther.Multi, "data": order, "page": pg, "multi_list": ex})
}

func FetchOrderSQL(c yee.Context) (err error) {
	u := c.QueryParam("k")
	var sql model.CoreSqlOrder
	var s []map[string]string
	model.DB().Select(" `sql`, `delay`, `id_c`, `source`,`data_base`,`table`, `text`, `type`, `work_id`").Where("work_id =?", u).First(&sql)
	sqlParser := parser.New()
	stmtNodes, _, err := sqlParser.Parse(sql.SQL, "", "")
	if err != nil {
		c.Logger().Error(err.Error())
		return err
	}
	for _, i := range stmtNodes {
		s = append(s, map[string]string{"SQL": i.Text()})
	}
	return c.JSON(http.StatusOK, singleOrder{
		CoreSqlOrder: sql,
		SQLS:         s,
	})
}

func RejectOrder(c yee.Context) (err error) {
	u := new(reject)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.Work).Updates(map[string]interface{}{"rejected": u.Text, "status": 0})
	lib.MessagePush(u.Work, 0, u.Text)
	return c.JSON(http.StatusOK, "工单已驳回！")
}

func ExecuteOrder(c yee.Context) (err error) {
	u := new(executeStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	var order model.CoreSqlOrder

	model.DB().Where("work_id =?", u.WorkId).First(&order)

	if order.Status != 2 && order.Status != 5 {
		c.Logger().Error("工单已执行过！操作不符合幂等性")
		return c.JSON(http.StatusOK, "工单已执行过！操作不符合幂等性")
	}

	executor := apis.Review{Order: order}

	executor.Init().Executor()

	return c.JSON(http.StatusOK, "工单已执行！")
}

func RollBackSQLOrder(c yee.Context) (err error) {
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
	lib.MessagePush(w, 2, "")
	return c.JSON(http.StatusOK, "工单已提交,请等待审核人审核！")
}

func MulitAuditOrder(c yee.Context) (err error) {
	req := new(executeStr)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", req.WorkId).Update(&model.CoreSqlOrder{Executor: req.Perform, Status: 5})
	lib.MessagePush(req.WorkId, 5, "")
	return c.JSON(200, "工单已提交执行人！")

}

func OscPercent(c yee.Context) (err error) {
	var k = &apis.OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, k.Percent())
}

func OscKill(c yee.Context) (err error) {
	var k = apis.OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, k.Kill())
}

func DelayKill(c yee.Context) (err error) {
	var k apis.Reviewer = &apis.Review{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, k.DelayKill())
}
