package query

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/handler/order/audit"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"github.com/cookieY/yee"
	"github.com/golang-jwt/jwt"
	"golang.org/x/net/websocket"
	"gorm.io/gorm"
	"io"
	"net/http"
	"time"
)

func FetchQueryOrder(c yee.Context) (err error) {
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		valid, err := lib.WSTokenIsValid(ws.Request().Header.Get("Sec-WebSocket-Protocol"))
		if err != nil {
			c.Logger().Error(err)
			return
		}
		if valid {
			var u common.PageList[[]model.CoreQueryOrder]
			var b []byte
			for {
				if err := websocket.Message.Receive(ws, &b); err != nil {
					if err != io.EOF {
						c.Logger().Error(err)
					}
					break
				}
				if string(b) == "ping" {
					continue
				}
				if err := json.Unmarshal(b, &u); err != nil {
					c.Logger().Error(err)
					break
				}
				token, err := lib.WsTokenParse(ws.Request().Header.Get("Sec-WebSocket-Protocol"))
				if err != nil {
					c.Logger().Error(err)
					break
				}
				is_record := token.Claims.(jwt.MapClaims)["is_record"].(bool)
				name := token.Claims.(jwt.MapClaims)["name"].(string)

				u.Paging().OrderBy("(status = 2) DESC, date DESC").Query(
					common.AccordingQueryToAssigned(c.QueryParam("tp") != "record" && is_record, name),
					common.AccordingToUsername(u.Expr.Username),
					common.AccordingToRealName(u.Expr.RealName),
					common.AccordingToDate(u.Expr.Picker),
					common.AccordingToWorkId(u.Expr.WorkId),
					common.AccordingToAllQueryOrderState(u.Expr.Status),
				)
				if err = websocket.Message.Send(ws, lib.ToJson(u.ToMessage())); err != nil {
					c.Logger().Error(err)
					break
				}
			}
		}
	}).ServeHTTP(c.Response(), c.Request())
	return nil
}

func FetchQueryRecordProfile(c yee.Context) (err error) {
	u := new(audit.Confirm)
	if err = c.Bind(u); err != nil {
		return
	}
	start, end := lib.Paging(u.Page, 15)
	l := new(common.GeneralList[[]model.CoreQueryRecord])
	model.DB().Model(&model.CoreQueryRecord{}).Where("work_id =?", u.WorkId).Count(&l.Page).Offset(start).Limit(end).Find(&l.Data)
	return c.JSON(http.StatusOK, l.ToMessage())
}

func QueryDeleteEmptyRecord(c yee.Context) (err error) {
	var j []model.CoreQueryOrder
	model.DB().Select("work_id").Where("`status` =?", 3).Find(&j)
	for _, i := range j {
		var k model.CoreQueryRecord
		if err := model.DB().Where("work_id =?", i.WorkId).First(&k).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
		}
	}
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_CLEAR)))
}

func QueryHandlerSets(c yee.Context) (err error) {
	u := new(common.QueryOrder)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	token := new(lib.Token).JwtParse(c)
	empty := new(model.CoreQueryOrder)
	found := model.DB().Where("work_id=? AND status=? AND assigned = ?", u.WorkId, 1, token.Username).Find(empty).Error
	switch c.Params("tp") {
	case "agreed":
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreQueryOrder{Status: 2, ApprovalTime: time.Now().Format("2006-01-02 15:04")})
			lib.MessagePush(u.WorkId, 8, "")
			return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_AGREE)))
		}
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	case "reject":
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreQueryOrder{Status: 4})
			lib.MessagePush(u.WorkId, 9, "")
			return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_REJECT)))
		}
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	case "undo":
		t := new(lib.Token)
		t.JwtParse(c)
		var order model.CoreQueryOrder
		model.DB().Model(model.CoreQueryOrder{}).Select("work_id").Where("username =?", t.Username).Last(&order)
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", order.WorkId).Updates(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_END)))
	case "stop":
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_END)))
	case "cancel":
		model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_ORDER_IS_ALL_END)))
	default:
		return
	}
}

func AuditQueryOrderProfileFetchApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "profile":
		return FetchQueryRecordProfile(c)
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}

func AuditQueryOrderApis(c yee.Context) (err error) {
	return FetchQueryOrder(c)
}
