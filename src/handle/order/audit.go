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

package order

import (
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/pingcap/parser"
	"net/http"
	"time"
)

type reject struct {
	Text string `json:"text"`
	Work string `json:"work"`
}

type referOrder struct {
	Data model.CoreSqlOrder `json:"data"`
	SQLs string             `json:"sqls"`
	Tp   int                `json:"tp"`
}

func FetchAuditOrder(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	user, _ := lib.JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	start, end := lib.Paging(u.Page, 20)

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToRelevant(user),
					commom.AccordingToUsername(u.Find.Text),
				).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		} else {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToRelevant(user),
					commom.AccordingToUsername(u.Find.Text),
					commom.AccordingToDatetime(u.Find.Picker),
				).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
		}
	} else {
		model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).Scopes(commom.AccordingToRelevant(user)).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": order, "page": pg})
}

func FetchPerformList(c yee.Context) (err error) {
	var ex []model.CoreAccount
	model.DB().Where("rule in (?)", []string{"admin", "super"}).Find(&ex)
	return c.JSON(http.StatusOK, map[string]interface{}{"perform": ex})
}

func FetchOrderSQL(c yee.Context) (err error) {
	u := c.QueryParam("k")
	var sql model.CoreSqlOrder
	var s []map[string]string
	model.DB().Where("work_id =?", u).First(&sql)
	sqlParser := parser.New()
	stmtNodes, _, err := sqlParser.Parse(sql.SQL, "", "")
	if err != nil {
		c.Logger().Error(err.Error())
		return err
	}
	for _, i := range stmtNodes {
		s = append(s, map[string]string{"sql": i.Text()})
	}
	return c.JSON(http.StatusOK, s)
}

func RejectOrder(c yee.Context) (err error) {
	u := new(reject)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)
	model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.Work).Updates(map[string]interface{}{"status": 0})
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.Work,
		Username: user,
		Rejected: u.Text,
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   "驳回",
	})
	lib.MessagePush(u.Work, 0, u.Text)
	return c.JSON(http.StatusOK, "工单已驳回！")
}

func ExecuteOrder(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)
	var order model.CoreSqlOrder

	model.DB().Where("work_id =?", u.WorkId).First(&order)

	if order.Status != 2 && order.Status != 5 {
		c.Logger().Error("工单已执行过！操作不符合幂等性")
		return c.JSON(http.StatusOK, "工单已执行过！操作不符合幂等性")
	}

	executor := new(Review)

	order.Assigned = user

	executor.Init(order).Executor()

	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user,
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   "审核通过并执行",
	})

	return c.JSON(http.StatusOK, "工单已执行！")
}

// RollBackSQLOrder create order record if order type of rollback
func RollBackSQLOrder(c yee.Context) (err error) {
	u := new(referOrder)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var sql string
	if u.Tp != 1 {
		sql = u.SQLs
	} else {
		var roll []model.CoreRollback
		model.DB().Select("`sql`").Where("work_id =?", u.Data.WorkId).Find(&roll)
		for _, i := range roll {
			sql += i.SQL + "\n"
		}
	}
	w := lib.GenWorkid()
	model.DB().Create(&model.CoreSqlOrder{
		WorkId:      w,
		Username:    u.Data.Username,
		Status:      2,
		Type:        u.Data.Type,
		Backup:      u.Data.Backup,
		IDC:         u.Data.IDC,
		Source:      u.Data.Source,
		DataBase:    u.Data.DataBase,
		Table:       u.Data.Table,
		Date:        time.Now().Format("2006-01-02 15:04"),
		SQL:         sql,
		Text:        u.Data.Text,
		Assigned:    u.Data.Assigned,
		Delay:       u.Data.Delay,
		RealName:    u.Data.RealName,
		CurrentStep: 1,
		Time:        time.Now().Format("2006-01-02"),
		Relevant:    lib.JsonStringify([]string{u.Data.Assigned}),
	})
	lib.MessagePush(w, 2, "")
	return c.JSON(http.StatusOK, "工单已提交,请等待审核人审核！")
}

func MultiAuditOrder(c yee.Context) (err error) {
	req := new(commom.ExecuteStr)
	user, _ := lib.JwtParse(c)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	model.DB().Exec("update core_sql_orders set relevant = JSON_ARRAY_APPEND(relevant, '$', ?), assigned = ? , current_step = ? where work_id =?", req.Perform, req.Perform, req.Flag+1, req.WorkId)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   req.WorkId,
		Username: user,
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   fmt.Sprintf("审核通过,并已转交至%s", req.Perform),
	})
	lib.MessagePush(req.WorkId, 5, "")
	return c.JSON(200, "工单已转交！")
}

// OscPercent show OSC percent
func OscPercent(c yee.Context) (err error) {
	var k = &OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, k.Percent())
}

// OscKill will kill OSC command
func OscKill(c yee.Context) (err error) {
	var k = OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, k.Kill())
}

//DelayKill will stop delay order
func DelayKill(c yee.Context) (err error) {
	return c.JSON(http.StatusOK, delayKill(c.Params("work_id")))
}

func FetchStepsDetail(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	var s []model.CoreWorkflowDetail
	model.DB().Where("work_id = ?", workId).Find(&s)
	return c.JSON(http.StatusOK, s)
}
