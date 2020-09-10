package order

import (
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func GeneralFetchMyOrder(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	start, end := lib.Paging(u.Page, 20)

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToGuest(user),
					commom.AccordingToText(u.Find.Text),
				).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		} else {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToGuest(user),
					commom.AccordingToText(u.Find.Text),
					commom.AccordingToDatetime(u.Find.Picker),
				).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
		}
	} else {
		model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).Scopes(commom.AccordingToGuest(user)).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"data": order, "page": pg, "multi": model.GloOther.Multi})
}
