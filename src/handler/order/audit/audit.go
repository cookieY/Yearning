package audit

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"golang.org/x/net/websocket"
	"net/http"
	"time"
)

func AuditOrderState(c yee.Context) (err error) {
	u := new(Confirm)
	user := new(lib.Token).JwtParse(c)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}

	switch u.Tp {
	case "undo":
		lib.MessagePush(u.WorkId, 6, "")
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Delete(&model.CoreSqlOrder{})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_UNDO))
	case "agree":
		return c.JSON(http.StatusOK, MultiAuditOrder(u, user.Username))
	case "reject":
		return c.JSON(http.StatusOK, RejectOrder(u, user.Username))
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}

//DelayKill will stop delay order
func DelayKill(c yee.Context) (err error) {
	u := new(Confirm)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	user := new(lib.Token).JwtParse(c)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user.Username,
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   ORDER_KILL_STATE,
	})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(delayKill(u.WorkId)))
}

func FetchAuditOrder(c yee.Context) (err error) {
	u := new(common.PageList[[]model.CoreSqlOrder])
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user := new(lib.Token).JwtParse(c)
	u.Paging().Query(common.AccordingToAllOrderState(u.Expr.Status),
		common.AccordingToAllOrderType(u.Expr.Type),
		common.AccordingToRelevant(user.Username),
		common.AccordingToText(u.Expr.Text),
		common.AccordingToUsernameEqual(u.Expr.Username),
		common.AccordingToDatetime(u.Expr.Picker))
	return c.JSON(http.StatusOK, u.ToMessage())
}

func FetchOSCAPI(c yee.Context) (err error) {
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		workId := c.QueryParam("work_id")
		var msg string
		for {
			if workId != "" {
				var osc model.CoreSqlOrder
				model.DB().Model(model.CoreOrderComment{}).Where("work_id =?", workId).Find(&osc)
				err := websocket.Message.Send(ws, osc.OSCInfo)
				if err != nil {
					c.Logger().Error(err)
					break
				}
			}
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				break
			}
			if msg == common.CLOSE {
				break
			}
		}
	}).ServeHTTP(c.Response(), c.Request())
	return nil
}

func AuditOrderApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "state":
		return AuditOrderState(c)
	case "kill":
		return DelayKill(c)
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}

func AuditOrRecordOrderFetchApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return FetchAuditOrder(c)
	//case "record":
	//	return FetchRecord(c)
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}

func AuditOSCFetchAndKillApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "osc":
		return FetchOSCAPI(c)
	case "kill":
		return nil
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}
