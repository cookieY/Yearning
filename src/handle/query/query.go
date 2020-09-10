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

package query

import (
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	ser "Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
	"net/http"
	"net/url"
	"time"
)

type queryOrder struct {
	IDC      string `json:"idc"`
	Source   string `json:"source"`
	Export   uint   `json:"export"`
	Assigned string `json:"assigned"`
	Text     string `json:"text"`
	WorkId   string `json:"work_id"`
}

func ReferQueryOrder(c yee.Context) (err error) {
	var u model.CoreAccount
	var t model.CoreQueryOrder
	user, _ := lib.JwtParse(c)

	d := new(queryOrder)
	if err = c.Bind(d); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}

	state := 1

	if model.GloOther.Query {
		state = 2
	}

	model.DB().Select("real_name").Where("username =?", user).First(&u)

	if model.DB().Model(model.CoreQueryOrder{}).Where("username =? and query_per =?", user, 2).First(&t).RecordNotFound() {

		work := lib.GenWorkid()

		model.DB().Create(&model.CoreQueryOrder{
			WorkId:   work,
			Username: user,
			Date:     time.Now().Format("2006-01-02 15:04"),
			Text:     d.Text,
			Assigned: d.Assigned,
			Export:   d.Export,
			IDC:      d.IDC,
			QueryPer: state,
			Realname: u.RealName,
			ExDate:   time.Now().Format("2006-01-02 15:04"),
		})

		if state == 2 {
			lib.MessagePush(work, 7, "")
		}

		return c.JSON(http.StatusOK, "查询工单已提交!")
	}
	return c.JSON(http.StatusOK, "重复提交!")
}

func FetchQueryStatus(c yee.Context) (err error) {

	user, _ := lib.JwtParse(c)

	var d model.CoreQueryOrder

	model.DB().Where("username =?", user).Last(&d)

	if lib.TimeDifference(d.ExDate) {
		model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{QueryPer: 3})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"status": d.QueryPer, "export": model.GloOther.Export, "idc": d.IDC})
}

func FetchQueryDatabaseInfo(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	var sign model.CoreGrained
	var groups []string

	model.DB().Where("username =?", user).Last(&d)

	model.DB().Where("username =?", user).First(&sign)

	if err := json.Unmarshal(sign.Group, &groups); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	ass := lib.MulitUserRuleMarge(groups)

	if d.QueryPer == 1 {

		var dc []string

		var mid []string

		source := new(queryOrder)

		if err = c.Bind(source); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, "")
		}

		model.DB().Where("source =?", source.Source).First(&u)

		result, err := commom.ScanDataRows(u, "", "SHOW DATABASES;", "库名", true)

		if err != nil {
			c.Logger().Error(err.Error())
			return err
		}

		if len(model.GloOther.ExcludeDbList) > 0 {
			mid = lib.Intersect(dc, model.GloOther.ExcludeDbList)
			dc = lib.NonIntersect(mid, dc)
		}

		var info []map[string]interface{}

		info = append(info, map[string]interface{}{
			"title":    source.Source,
			"expand":   "true",
			"children": result.BaseList,
		})

		return c.JSON(http.StatusOK, map[string]interface{}{"info": info, "status": d.Export, "highlight": result.Highlight, "sign": ass.Auditor})

	} else {
		return c.JSON(http.StatusOK, 0)
	}
}

func FetchQueryTableInfo(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	t := c.QueryParam("t")
	// todo source改方法 不然中文无法识别
	source := c.QueryParam("source")
	unescape, _ := url.QueryUnescape(source)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	model.DB().Where("username =?", user).Last(&d)

	if d.QueryPer == 1 {

		model.DB().Where("source =?", unescape).First(&u)

		result, err := commom.ScanDataRows(u, t, "SHOW TABLES;", "表名", true)
		if err != nil {
			c.Logger().Error(err.Error())
			return err
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"table": result.Query, "highlight": result.Highlight})

	} else {
		return c.JSON(http.StatusOK, 0)
	}
}

func FetchQueryTableStruct(c yee.Context) (err error) {
	t := c.QueryParam("table")
	b := c.QueryParam("base")
	source := c.QueryParam("source")
	unescape, _ := url.QueryUnescape(source)
	user, _ := lib.JwtParse(c)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	var f []ser.FieldInfo
	model.DB().Where("username =?", user).Last(&d)
	model.DB().Where("source =?", unescape).First(&u)
	ps := lib.Decrypt(u.Password)

	db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local", u.Username, ps, u.IP, u.Port, b))
	if e != nil {
		c.Logger().Error(e.Error())
		return c.JSON(http.StatusInternalServerError, "数据库实例连接失败！请检查相关配置是否正确！")
	}
	defer db.Close()

	if err := db.Raw(fmt.Sprintf("SHOW FULL FIELDS FROM `%s`.`%s`", b, t)).Scan(&f).Error; err != nil {
		c.Logger().Error(err.Error())
	}

	return c.JSON(http.StatusOK, f)
}

func FetchQueryResults(c yee.Context) (err error) {

	req := new(model.Queryresults)

	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}

	var d model.CoreQueryOrder

	var u model.CoreDataSource

	user, _ := lib.JwtParse(c)

	model.DB().Where("username =? AND query_per =?", user, 1).Last(&d)

	if lib.TimeDifference(d.ExDate) {
		model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{QueryPer: 3})
		return c.JSON(http.StatusOK, map[string]interface{}{"status": true})
	}
	model.DB().Where("source =?", req.Source).First(&u)

	//需自行实现查询SQL LIMIT限制

	r, err := lib.ExQuery(&pb.LibraAuditOrder{SQL: req.Sql})

	if err != nil {
		return c.JSON(http.StatusOK, err.Error())
	}

	req.Sql = r.SQL

	//结束
	t1 := time.Now()
	data, err := lib.QueryMethod(&u, req, r.InsulateWordList)

	if err != nil {
		return c.JSON(http.StatusOK, err.Error())
	}

	queryTime := int(time.Since(t1).Seconds() * 1000)

	go func(w string, s string, ex int) {
		model.DB().Create(&model.CoreQueryRecord{SQL: s, WorkId: w, ExTime: ex, Time: time.Now().Format("2006-01-02 15:04"), Source: req.Source, BaseName: req.Basename})
	}(d.WorkId, req.Sql, queryTime)

	return c.JSON(http.StatusOK, map[string]interface{}{"title": data.Field, "data": data.Data, "status": false, "time": queryTime})
}

func UndoQueryOrder(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(map[string]interface{}{"query_per": 3})
	return c.JSON(http.StatusOK, "查询已终止！")
}

func FetchQueryOrder(c yee.Context) (err error) {

	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	start, end := lib.Paging(u.Page, 20)
	var pg int

	var order []model.CoreQueryOrder

	user, _ := lib.JwtParse(c)

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Model(model.CoreQueryOrder{}).Scopes(
				commom.AccordingToUsername(u.Find.Text),
				commom.AccordingToAssigned(user),
			).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		} else {
			model.DB().Model(model.CoreQueryOrder{}).Scopes(
				commom.AccordingToUsername(u.Find.Text),
				commom.AccordingToAssigned(user),
				commom.AccordingToDatetime(u.Find.Picker),
			).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		}
	} else {
		model.DB().Model(model.CoreQueryOrder{}).Scopes(commom.AccordingToAssigned(user)).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": order, "page": pg})
}

func QueryDeleteEmptyRecord(c yee.Context) (err error) {
	var j []model.CoreQueryOrder
	model.DB().Select("work_id").Where(`query_per =?`, 3).Find(&j)
	for _, i := range j {
		var k model.CoreQueryRecord
		if model.DB().Where("work_id =?", i.WorkId).First(&k).RecordNotFound() {
			model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
		}
	}
	return c.JSON(http.StatusOK, "空查询工单已全部清除！")
}

func QueryHandlerSets(c yee.Context) (err error) {
	param := c.Params("tp")
	u := new(queryOrder)
	var s model.CoreQueryOrder
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	found := !model.DB().Where("work_id=? AND query_per=?", u.WorkId, 2).Last(&s).RecordNotFound()

	switch param {
	case "agreed":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 1, "ex_date": time.Now().Format("2006-01-02 15:04")})
			lib.MessagePush(u.WorkId, 8, "")
		}
		return c.JSON(http.StatusOK, "该次工单查询已同意！")
	case "disagreed":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 0})
			lib.MessagePush(u.WorkId, 9, "")
		}
		return c.JSON(http.StatusOK, "该次工单查询已驳回！")
	case "stop":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", s.WorkId).Update(map[string]interface{}{"query_per": 3})
		}
		return c.JSON(http.StatusOK, "查询已终止！")
	default:
		model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{QueryPer: 3})
		return c.JSON(http.StatusOK, "所有查询已取消！")
	}
}
