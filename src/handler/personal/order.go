package personal

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func PersonalFetchMyOrder(c yee.Context) (err error) {
	u := new(commom.PageChange)
	if err = c.Bind(u); err != nil {
		return
	}
	user := new(lib.Token).JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	start, end := lib.Paging(u.Current, u.PageSize)

	model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
		Scopes(
			commom.AccordingToAllOrderType(u.Expr.Type),
			commom.AccordingToAllOrderState(u.Expr.Status),
			commom.AccordingToUsernameEqual(user.Username),
			commom.AccordingToDatetime(u.Expr.Picker),
			commom.AccordingToText(u.Expr.Text),
		).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: order, Page: pg}))
}

func PersonalUserEdit(c yee.Context) (err error) {
	u := new(model.CoreAccount)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	user := new(lib.Token).JwtParse(c)
	if u.Password == "" {
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user.Username).Update(
			&model.CoreAccount{
				Email:      u.Email,
				RealName:   u.RealName,
				Department: u.Department,
			})
	} else {
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user.Username).Update(
			&model.CoreAccount{
				Password:   lib.DjangoEncrypt(u.Password, string(lib.GetRandom())),
				Email:      u.Email,
				RealName:   u.RealName,
				Department: u.Department,
			})
	}

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(CUSTOM_PASSWORD_SUCCESS))
}

func Put(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return PersonalFetchMyOrder(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}
