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
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/net/websocket"
	"io"
	"net/http"
	"net/url"
	"time"
)

type queryResults struct {
	Export    bool     `msgpack:"export"`
	Error     string   `msgpack:"error"`
	Results   []*Query `msgpack:"results"`
	QueryTime int      `msgpack:"query_time"`
	Status    bool     `msgpack:"status"`
}

func reflect(flag bool) uint {
	if flag {
		return 1
	}
	return 0
}

func ReferQueryOrder(c yee.Context, user *lib.Token) (err error) {
	var t model.CoreQueryOrder
	d := new(commom.QueryOrder)
	if err = c.Bind(d); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	work := lib.GenWorkid()
	if !model.GloOther.Query {
		model.DB().Create(&model.CoreQueryOrder{
			WorkId:       work,
			Username:     user.Username,
			Date:         time.Now().Format("2006-01-02 15:04"),
			Export:       reflect(model.GloOther.Export),
			Status:       2,
			RealName:     user.RealName,
			Text:         "当前未开启查询审核,用户可自由查询",
			Assigned:     "admin",
			ApprovalTime: time.Now().Format("2006-01-02 15:04"),
		})
		return
	}

	if model.DB().Model(model.CoreQueryOrder{}).Where("username =? and status =?", user.Username, 2).First(&t).RecordNotFound() {
		var principal model.CoreDataSource
		model.DB().Model(model.CoreDataSource{}).Where("source_id = ?", d.SourceId).First(&principal)
		model.DB().Create(&model.CoreQueryOrder{
			WorkId:   work,
			Username: user.Username,
			Date:     time.Now().Format("2006-01-02 15:04"),
			Text:     d.Text,
			Export:   d.Export,
			Status:   1,
			SourceId: d.SourceId,
			Assigned: principal.Principal,
			RealName: user.RealName,
		})
		lib.MessagePush(work, 7, "")
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_CREATE))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_DUP))
}

func FetchQueryDatabaseInfo(c yee.Context) (err error) {
	var d model.CoreQueryOrder
	var u model.CoreDataSource

	model.DB().Where("source_id =?", c.QueryParam("source_id")).First(&u)

	result, err := commom.ScanDataRows(u, "", "SHOW DATABASES;", "Schema", true, false)

	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"info": result.QueryList, "status": d.Export}))
}

func FetchQueryTableInfo(c yee.Context) (err error) {

	t := c.QueryParam("schema")
	// todo source改方法 不然中文无法识别
	source := c.QueryParam("source_id")
	unescape, _ := url.QueryUnescape(source)

	var u model.CoreDataSource

	model.DB().Where("source_id =?", unescape).First(&u)

	result, err := commom.ScanDataRows(u, t, "SHOW TABLES;", "Table", true, true)
	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"table": result.QueryList}))

}

func FetchQueryTableStruct(c yee.Context) (err error) {

	t := new(queryBind)

	if err := c.Bind(t); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	unescape, _ := url.QueryUnescape(t.Source)

	var u model.CoreDataSource

	var f []commom.FieldInfo

	model.DB().Where("source =?", unescape).First(&u)

	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local", u.Username, lib.Decrypt(u.Password), u.IP, u.Port, t.DataBase))
	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, commom.SuccessPayLoadToMessage(ER_DB_CONNENT))
	}
	defer db.Close()

	if err := db.Raw(fmt.Sprintf("SHOW FULL FIELDS FROM `%s`.`%s`", t.DataBase, t.Table)).Scan(&f).Error; err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}

	return c.JSON(http.StatusOK, commom.SuccessPayload(f))
}

func SocketQueryResults(c yee.Context) (err error) {
	user := c.QueryParam("user")
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		var b []byte
		valid, err := lib.WSTokenIsValid(ws.Request().Header.Get("Sec-WebSocket-Protocol"))
		if err != nil {
			c.Logger().Error(err)
			return
		}
		if valid {
			for {
				var msg QueryDeal
				if err := websocket.Message.Receive(ws, &b); err != nil {
					if err != io.EOF {
						c.Logger().Error(err)
					}
					break
				}

				if err := msgpack.Unmarshal(b, &msg.Ref); err != nil {
					c.Logger().Error(err)
					break
				}

				switch msg.Ref.Type {
				case commom.CLOSE:
					break
				default:
					var d model.CoreQueryOrder
					clock := time.Now()
					if model.DB().Where("username =? AND status =?", user, 2).Last(&d).RecordNotFound() {
						if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: "查询工单获取失败或工单已过期,请返回上一级菜单"})); err != nil {
							c.Logger().Error(err)
						}
						continue
					}

					if lib.TimeDifference(d.ApprovalTime) {
						model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Update(&model.CoreQueryOrder{Status: 3})
						if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Status: true})); err != nil {
							c.Logger().Error(err)
						}
						continue
					}

					var u model.CoreDataSource

					var queryData []*Query

					model.DB().Where("source_id =?", msg.Ref.SourceId).First(&u)

					if err := msg.PreCheck(u.InsulateWordList); err != nil {
						if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: err.Error()})); err != nil {
							c.Logger().Error(err)
						}
						continue
					}

					for _, i := range msg.MultiSQLRunner {
						result, err := i.Run(&u, msg.Ref.Schema)
						if err != nil {
							if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: err.Error()})); err != nil {
								c.Logger().Error(err)
							}
							goto NEXT
						}
						queryData = append(queryData, result)
					}

					queryTime := int(time.Since(clock).Seconds() * 1000)

					go func(w string, s string, ex int) {
						model.DB().Create(&model.CoreQueryRecord{SQL: s, WorkId: w, ExTime: ex, Time: time.Now().Format("2006-01-02 15:04"), Source: u.Source, Schema: msg.Ref.Schema})
					}(d.WorkId, msg.Ref.Sql, queryTime)
					if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Export: d.Export == 1, Error: "", Results: queryData, QueryTime: queryTime})); err != nil {
						c.Logger().Error(err)
					}
				}
			NEXT:
				continue
			}
		}

	}).ServeHTTP(c.Response(), c.Request())
	return nil
}

func UndoQueryOrder(c yee.Context) (err error) {
	user := new(lib.Token).JwtParse(c)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user.Username).Update(map[string]interface{}{"status": 3})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_END))
}
