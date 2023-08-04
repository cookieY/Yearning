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
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"errors"
	"fmt"
	"github.com/cookieY/sqlx"
	"github.com/cookieY/yee"
	"github.com/golang-jwt/jwt"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/net/websocket"
	"gorm.io/gorm"
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
	HeartBeat string   `msgpack:"heartbeat"`
	IsOnly    bool     `msgpack:"is_only"`
}

type queryArgs struct {
	SourceId string `json:"source_id"`
}

type queryCore struct {
	db               *sqlx.DB
	insulateWordList string
	source           string
}

func reflect(flag bool) uint {
	if flag {
		return 1
	}
	return 0
}

func ReferQueryOrder(c yee.Context, user *lib.Token) (err error) {
	var t model.CoreQueryOrder
	d := new(common.QueryOrder)
	if err = c.Bind(d); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
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
			Text:         i18n.DefaultLang.Load(i18n.INFO_QUERY_AUDIT_DISABLED),
			Assigned:     "admin",
			ApprovalTime: time.Now().Format("2006-01-02 15:04"),
		})
		return
	}

	if err := model.DB().Model(model.CoreQueryOrder{}).Where("username =? and status =?", user.Username, 2).First(&t).Error; errors.Is(err, gorm.ErrRecordNotFound) {
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
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_CREATE)))
	}
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_DUP)))
}

func FetchQueryDatabaseInfo(c yee.Context) (err error) {
	var u model.CoreDataSource

	model.DB().Where("source_id =?", c.QueryParam("source_id")).First(&u)

	result, err := common.ScanDataRows(u, "", "SHOW DATABASES;", "Schema", true, false)

	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, common.SuccessPayload(result.QueryList))
}

func FetchQueryTableInfo(c yee.Context) (err error) {

	t := c.QueryParam("schema")
	// todo source改方法 不然中文无法识别
	source := c.QueryParam("source_id")
	unescape, _ := url.QueryUnescape(source)

	var u model.CoreDataSource

	model.DB().Where("source_id =?", unescape).First(&u)

	result, err := common.ScanDataRows(u, t, "SHOW TABLES;", "Table", true, true)
	if err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, common.SuccessPayload(map[string]interface{}{"table": result.QueryList}))

}

func SocketQueryResults(c yee.Context) (err error) {
	args := new(queryArgs)
	if err = c.Bind(args); err != nil {
		return
	}
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		var b []byte
		token, err := lib.WsTokenParse(ws.Request().Header.Get("Sec-WebSocket-Protocol"))
		if err != nil {
			c.Logger().Error(err)
			return
		}
		user := token.Claims.(jwt.MapClaims)["name"].(string)
		control := lib.SourceControl{User: user, Kind: lib.QUERY, SourceId: args.SourceId}
		if !control.Equal() {
			c.Logger().Criticalf(i18n.DefaultLang.Load(i18n.ER_USER_NO_PERMISSION), user, args.SourceId)
			return
		}

		if token.Valid {
			msg := new(QueryDeal)
			core := new(queryCore)
			var u model.CoreDataSource
			model.DB().Where("source_id =?", args.SourceId).First(&u)
			core.db, err = sqlx.Connect("mysql", fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4", u.Username, lib.Decrypt(model.JWT, u.Password), u.IP, u.Port))
			if err != nil {
				c.Logger().Error(err)
				_ = websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: err.Error()}))
				return
			}
			core.insulateWordList = u.InsulateWordList
			core.source = u.Source
			defer core.db.Close()
			for {
				if err := websocket.Message.Receive(ws, &b); err != nil {
					if err != io.EOF {
						c.Logger().Error(err)
					}
					break
				}
				if string(b) == "ping" {
					_ = websocket.Message.Send(ws, lib.ToMsg(queryResults{HeartBeat: common.Pong, IsOnly: model.GloOther.Query}))
					continue
				}
				if err := msgpack.Unmarshal(b, &msg.Ref); err != nil {
					c.Logger().Error(err)
					break
				}
				var d model.CoreQueryOrder
				msg.MultiSQLRunner = []MultiSQLRunner{}
				clock := time.Now()
				if err := model.DB().Where("username =? AND status =?", user, 2).Last(&d).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Status: true})); err != nil {
						c.Logger().Error(err)
					}
					continue
				}

				if lib.TimeDifference(d.ApprovalTime) {
					model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user).Updates(&model.CoreQueryOrder{Status: 3})
					if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Status: true})); err != nil {
						c.Logger().Error(err)
					}
					continue
				}

				var queryData []*Query

				if err := msg.PreCheck(core.insulateWordList); err != nil {
					if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: err.Error()})); err != nil {
						c.Logger().Error(err)
					}
					continue
				}

				for _, i := range msg.MultiSQLRunner {
					result, err := i.Run(core.db, msg.Ref.Schema)
					if err != nil {
						if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Error: err.Error()})); err != nil {
							c.Logger().Error(err)
						}
						continue
					}

					queryData = append(queryData, result)
				}

				queryTime := int(time.Since(clock).Seconds() * 1000)
				go func(w string, s string, ex int) {
					model.DB().Create(&model.CoreQueryRecord{SQL: s, WorkId: w, ExTime: ex, Time: time.Now().Format("2006-01-02 15:04"), Source: core.source, Schema: msg.Ref.Schema})
				}(d.WorkId, msg.Ref.Sql, queryTime)
				if err := websocket.Message.Send(ws, lib.ToMsg(queryResults{Export: d.Export == 1, Results: queryData, QueryTime: queryTime})); err != nil {
					c.Logger().Error(err)
				}
			}
		}

	}).ServeHTTP(c.Response(), c.Request())
	return nil
}

func UndoQueryOrder(c yee.Context) (err error) {
	user := new(lib.Token).JwtParse(c)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user.Username).Updates(map[string]interface{}{"status": 3})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_END)))
}
