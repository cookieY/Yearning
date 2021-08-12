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

package personal

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/handler/fetch"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	ser "Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
)

func ReferQueryOrder(c yee.Context, user *string) (err error) {
	var u model.CoreAccount
	var t model.CoreQueryOrder

	d := new(commom.QueryOrder)
	if err = c.Bind(d); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
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
			Username: *user,
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

		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_CREATE))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_DUP))
}

func FetchQueryStatus(c yee.Context) (err error) {

	user, _ := lib.JwtParse(c)

	var d model.CoreQueryOrder

	model.DB().Where("username =?", user).Last(&d)

	if lib.TimeDifference(d.ExDate) {
		model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{QueryPer: 3})
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"status": d.QueryPer, "export": model.GloOther.Export, "idc": d.IDC}))
}

func FetchQueryDatabaseInfo(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	var d model.CoreQueryOrder
	var u model.CoreDataSource

	model.DB().Where("username =?", user).Last(&d)

	if d.QueryPer == 1 {

		source := new(commom.QueryOrder)

		if err = c.Bind(source); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
		}

		model.DB().Where("source =?", source.Source).First(&u)

		result, err := commom.ScanDataRows(u, "", "SHOW DATABASES;", "库名", true)

		if err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
		}

		var info []map[string]interface{}

		info = append(info, map[string]interface{}{
			"title":    source.Source,
			"expand":   "true",
			"children": result.BaseList,
		})
		return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"info": info, "status": d.Export, "highlight": result.Highlight, "sign": fetch.FetchTplAuditor(u.IDC), "idc": u.IDC}))

	} else {
		return c.JSON(http.StatusOK, commom.SuccessPayload(0))
	}
}

func FetchQueryTableInfo(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	t := c.QueryParam("title")
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
			return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
		}
		return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"table": result.Query, "highlight": result.Highlight}))

	} else {
		return c.JSON(http.StatusOK, commom.SuccessPayload(0))
	}
}

func FetchQueryTableStruct(c yee.Context) (err error) {
	t := new(queryBind)
	if err := c.Bind(t); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	unescape, _ := url.QueryUnescape(t.Source)
	user, _ := lib.JwtParse(c)
	var d model.CoreQueryOrder
	var u model.CoreDataSource
	var f []ser.FieldInfo
	model.DB().Where("username =?", user).Last(&d)
	model.DB().Where("source =?", unescape).First(&u)
	ps := lib.Decrypt(u.Password)

	db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local", u.Username, ps, u.IP, u.Port, t.DataBase))
	if e != nil {
		c.Logger().Error(e.Error())
		return c.JSON(http.StatusInternalServerError, commom.SuccessPayLoadToMessage(ER_DB_CONNENT))
	}
	defer db.Close()

	if err := db.Raw(fmt.Sprintf("SHOW FULL FIELDS FROM `%s`.`%s`", t.DataBase, t.Table)).Scan(&f).Error; err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}

	return c.JSON(http.StatusOK, commom.SuccessPayload(f))
}

func FetchQueryResults(c yee.Context, user *string) (err error) {

	req := new(lib.QueryDeal)

	clock := time.Now()

	if err = c.Bind(req); err != nil {
		return c.JSON(http.StatusOK, err.Error())
	}

	//需自行实现查询SQL LIMIT限制
	err = req.Limit(&pb.LibraAuditOrder{SQL: req.Sql})

	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}

	var d model.CoreQueryOrder

	model.DB().Where("username =? AND query_per =?", user, 1).Last(&d)

	if lib.TimeDifference(d.ExDate) {
		model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{QueryPer: 3})
		return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"status": true}))
	}

	//结束
	data := new(lib.Query)

	var u model.CoreDataSource

	model.DB().Where("source =?", req.Source).First(&u)

	err = data.QueryRun(&u, req)

	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}

	queryTime := int(time.Since(clock).Seconds() * 1000)

	go func(w, s string, ex int) {
		model.DB().Create(&model.CoreQueryRecord{SQL: s, WorkId: w, ExTime: ex, Time: time.Now().Format("2006-01-02 15:04"), Source: req.Source, BaseName: req.DataBase})
	}(d.WorkId, req.Sql, queryTime)

	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"title": data.Field, "data": data.Data, "status": false, "time": queryTime, "total": len(data.Data)}))
}

func UndoQueryOrder(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(map[string]interface{}{"query_per": 3})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_END))
}
