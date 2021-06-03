package personal

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func PersonalFetchMyOrder(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	user, _ := lib.JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	start, end := lib.Paging(u.Page, 15)

	model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
		Scopes(
			commom.AccordingToAllOrderState(u.Find.Status),
			commom.AccordingToUsernameEqual(user),
			commom.AccordingToDatetime(u.Find.Picker),
			commom.AccordingToText(u.Find.Text),
		).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: order, Page: pg, Multi: model.GloOther.Multi}))
}

func PersonalUserEdit(c yee.Context) (err error) {
	param := c.QueryParam("tp")
	u := new(model.CoreAccount)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	user, _ := lib.JwtParse(c)
	switch param {
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user).Update(
			&model.CoreAccount{Password: lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(CUSTOM_PASSWORD_SUCCESS))
	case "mail":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user).Updates(model.CoreAccount{Email: u.Email, RealName: u.RealName})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(CUSTOM_INFO_SUCCESS))
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}

func PersonalFetchOrderListOrProfile(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return PersonalFetchMyOrder(c)
	case "edit":
		return PersonalUserEdit(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}
