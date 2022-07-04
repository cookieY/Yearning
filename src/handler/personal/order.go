package personal

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func PersonalFetchMyOrder(c yee.Context) (err error) {
	var u = new(common.PageList[[]model.CoreSqlOrder])
	if err = c.Bind(u); err != nil {
		return
	}
	user := new(lib.Token).JwtParse(c)
	u.Paging().Select(common.QueryField).Query(
		common.AccordingToAllOrderType(u.Expr.Type),
		common.AccordingToAllOrderState(u.Expr.Status),
		common.AccordingToUsernameEqual(user.Username),
		common.AccordingToDatetime(u.Expr.Picker),
		common.AccordingToText(u.Expr.Text),
	)
	return c.JSON(http.StatusOK, u.ToMessage())
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

func Put(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return PersonalFetchMyOrder(c)
	default:
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}
