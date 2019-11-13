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
	ser "Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"encoding/json"
	"fmt"
	"github.com/jinzhu/gorm"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/url"
	"time"
)

type queryOrder struct {
	IDC      string
	Source   string
	Export   uint
	Assigned string
	Text     string
	WorkId   string
}

type clearQueryOrder struct {
	WorkId []string
}

func ReferQueryOrder(c echo.Context) (err error) {
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

		lib.MessagePush(c, work, 6, "")

		return c.JSON(http.StatusOK, "查询工单已提交!")
	}
	return c.JSON(http.StatusOK, "重复提交!")
}

func FetchQueryStatus(c echo.Context) (err error) {

	user, _ := lib.JwtParse(c)

	var d model.CoreQueryOrder

	model.DB().Where("username =?", user).Last(&d)

	if lib.TimeDifference(d.ExDate) {
		model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{QueryPer: 3})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"status": d.QueryPer, "export": model.GloOther.Export, "idc": d.IDC})
}

func FetchQueryDatabaseInfo(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	var sign model.CoreGrained
	var ass model.PermissionList
	model.DB().Where("username =?", user).Last(&d)

	model.DB().Where("username =?", user).First(&sign)

	if err := json.Unmarshal(sign.Permissions, &ass); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	if d.QueryPer == 1 {

		var dataBase string

		var dc [] string

		var mid [] string

		var highlist []map[string]string

		var baselist []map[string]interface{}

		source := new(queryOrder)
		if err = c.Bind(source); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, "")
		}

		model.DB().Where("source =?", source.Source).First(&u)

		ps := lib.Decrypt(u.Password)

		db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", u.Username, ps, u.IP, u.Port))

		defer db.Close()

		if e != nil {
			c.Logger().Error(e.Error())
			return c.JSON(http.StatusInternalServerError, "数据库实例连接失败！请检查相关配置是否正确！")
		}

		sql := "SHOW DATABASES;"
		rows, err := db.Raw(sql).Rows()
		if err != nil {
			c.Logger().Error(err.Error())
		}
		defer rows.Close()

		for rows.Next() {
			rows.Scan(&dataBase)
			dc = append(dc, dataBase)
		}

		if len(model.GloOther.ExcludeDbList) > 0 {
			mid = lib.Intersect(dc, model.GloOther.ExcludeDbList)
			dc = lib.NonIntersect(mid, dc)
		}

		for _, z := range dc {
			highlist = append(highlist, map[string]string{"vl": z, "meta": "库名"})
			baselist = append(baselist, map[string]interface{}{"title": z, "children": []map[string]string{{}}})
		}

		var info [] map[string]interface{}

		info = append(info, map[string]interface{}{
			"title":    source.Source,
			"expand":   "true",
			"children": baselist,
		})

		return c.JSON(http.StatusOK, map[string]interface{}{"info": info, "status": d.Export, "highlight": highlist, "sign": ass.Auditor})

	} else {
		return c.JSON(http.StatusOK, 0)
	}
}

func FetchQueryTableInfo(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	t := c.Param("t")
	// todo source改方法 不然中文无法识别
	source := c.Param("source")
	unescape, _ := url.QueryUnescape(source)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	model.DB().Where("username =?", user).Last(&d)

	if d.QueryPer == 1 {

		var table string

		var highlist []map[string]string

		var tablelist []map[string]interface{}

		model.DB().Where("source =?", unescape).First(&u)

		ps := lib.Decrypt(u.Password)

		db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local", u.Username, ps, u.IP, u.Port, t))

		defer db.Close()

		if e != nil {
			c.Logger().Error(e.Error())
			return c.JSON(http.StatusInternalServerError, "数据库实例连接失败！请检查相关配置是否正确！")
		}

		sql := "show tables"
		rows, err := db.Raw(sql).Rows()
		if err != nil {
			c.Logger().Error(err.Error())
		}
		defer rows.Close()
		for rows.Next() {
			rows.Scan(&table)
			highlist = append(highlist, map[string]string{"vl": table, "meta": "表名"})
			tablelist = append(tablelist, map[string]interface{}{"title": table})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{"table": tablelist, "highlight": highlist})

	} else {
		return c.JSON(http.StatusOK, 0)
	}
}

func FetchQueryTableStruct(c echo.Context) (err error) {
	t := c.Param("table")
	b := c.Param("base")
	source := c.Param("source")
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

func FetchQueryResults(c echo.Context) (err error) {

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

	r := lib.ExQuery(&pb.LibraAuditOrder{SQL: req.Sql})

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

func AgreedQueryOrder(c echo.Context) (err error) {
	u := new(queryOrder)
	var s model.CoreQueryOrder
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}

	if model.DB().Where("work_id=? AND query_per=?", u.WorkId, 2).Last(&s).RecordNotFound() {
		return c.JSON(http.StatusOK, "工单状态已变更！")
	}

	model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 1, "ex_date": time.Now().Format("2006-01-02 15:04")})
	lib.MessagePush(c, u.WorkId, 7, "")
	return c.JSON(http.StatusOK, "该次工单查询已同意！")
}

func DisAgreedQueryOrder(c echo.Context) (err error) {
	u := new(queryOrder)
	var s model.CoreQueryOrder
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}

	if model.DB().Where("work_id=? AND query_per=?", u.WorkId, 2).Last(&s).RecordNotFound() {
		return c.JSON(http.StatusOK, "工单状态已变更！")
	}

	model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 0})
	lib.MessagePush(c, u.WorkId, 8, "")
	return c.JSON(http.StatusOK, "该次工单查询已驳回！")
}

func DelQueryOrder(c echo.Context) (err error) {
	req := new(clearQueryOrder)
	if err := c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	var d model.CoreQueryOrder

	tx := model.DB().Begin()
	for _, i := range req.WorkId {
		model.DB().Where("work_id =?", i).Delete(&model.CoreQueryOrder{})
		model.DB().Where("work_id =?", d.WorkId).Delete(&model.CoreQueryRecord{})
	}
	tx.Commit()

	return c.JSON(http.StatusOK, "查询工单删除!")
}

func UndoQueryOrder(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(map[string]interface{}{"query_per": 3})
	return c.JSON(http.StatusOK, "查询已终止！")
}

func SuperUndoQueryOrder(c echo.Context) (err error) {
	s := new(queryOrder)
	var u model.CoreQueryOrder
	if err = c.Bind(s); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}

	if !model.DB().Where("work_id=? AND query_per=?", s.WorkId, 2).Last(&u).RecordNotFound() {
		return c.JSON(http.StatusOK, "工单状态已变更！")
	}

	model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", s.WorkId).Update(map[string]interface{}{"query_per": 3})
	return c.JSON(http.StatusOK, "查询已终止！")
}

func FetchQueryOrder(c echo.Context) (err error) {
	u := new(f)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	start, end := lib.Paging(u.Page, 20)
	var pg int

	var order []model.CoreQueryOrder

	whereField := "username LIKE ? "

	dateField := " AND date >= ? AND date <= ?"

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Where(whereField, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreQueryOrder{}).Where(whereField, "%"+fmt.Sprintf("%s", u.Find.User)+"%").Count(&pg)
		} else {
			model.DB().Where(whereField+dateField, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreQueryOrder{}).Where(whereField+dateField, "%"+fmt.Sprintf("%s", u.Find.User)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg)
		}
	} else {
		model.DB().Order("id desc").Offset(start).Limit(end).Find(&order)
		model.DB().Model(&model.CoreQueryOrder{}).Count(&pg)
	}
	return c.JSON(http.StatusOK, struct {
		Data []model.CoreQueryOrder `json:"data"`
		Page int                    `json:"page"`
	}{
		order,
		pg,
	})
}

func QueryQuickCancel(c echo.Context) (err error) {
	model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{QueryPer: 3})
	return c.JSON(http.StatusOK, "所有查询已取消！")
}
