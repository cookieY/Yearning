package personal

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"github.com/golang-jwt/jwt"
	"golang.org/x/net/websocket"
	"io"
	"net/http"
)

func PersonalFetchMyOrder(c yee.Context) (err error) {
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		valid, err := lib.WSTokenIsValid(ws.Request().Header.Get("Sec-WebSocket-Protocol"))
		if err != nil {
			c.Logger().Error(err)
			return
		}
		if valid {
			var u common.PageList[[]model.CoreSqlOrder]
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
				user := token.Claims.(jwt.MapClaims)["name"].(string)
				u.Paging().Select(common.QueryField).Query(
					common.AccordingToAllOrderType(u.Expr.Type),
					common.AccordingToAllOrderState(u.Expr.Status),
					common.AccordingToUsernameEqual(user),
					common.AccordingToDate(u.Expr.Picker),
					common.AccordingToText(u.Expr.Text),
					common.AccordingToWorkId(u.Expr.WorkId),
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

func PersonalUserEdit(c yee.Context) (err error) {
	u := new(model.CoreAccount)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	user := new(lib.Token).JwtParse(c)
	if u.Password == "" {
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user.Username).Updates(
			&model.CoreAccount{
				Email:      u.Email,
				RealName:   u.RealName,
				Department: u.Department,
			})
	} else {
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user.Username).Updates(
			&model.CoreAccount{
				Password:   lib.DjangoEncrypt(u.Password, string(lib.GetRandom())),
				Email:      u.Email,
				RealName:   u.RealName,
				Department: u.Department,
			})
	}

	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(CUSTOM_PASSWORD_SUCCESS))
}

func Get(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return PersonalFetchMyOrder(c)
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}
