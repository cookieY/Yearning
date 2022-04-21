package record

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
	"time"
)

type groupBy struct {
	C    int    `json:"count"`
	Time string `json:"time"`
	Type string `json:"type"`
}

type count struct {
	DDL int `json:"ddl"`
	DML int `json:"dml"`
}

func timeAdd(add string) string {
	m, _ := time.ParseDuration(fmt.Sprintf("%sh", add))
	return time.Now().Add(m).Format("2006-01-02")
}

func RecordDashAxis(c yee.Context) (err error) {
	var order []groupBy
	var count count
	model.DB().Model(model.CoreSqlOrder{}).Where("time > ? and type = 1", timeAdd("-2160")).Count(&count.DML)
	model.DB().Model(model.CoreSqlOrder{}).Where("time > ? and type = 0", timeAdd("-2160")).Count(&count.DDL)
	model.DB().Model(model.CoreSqlOrder{}).Select("time, count(*) as c,type").Where("time > ?", timeAdd("-2160")).Group("time,type").Scan(&order)
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"order": order, "count": count}))
}

func RecordOrderList(c yee.Context) (err error) {
	u := new(commom.PageChange)
	if err = c.Bind(u); err != nil {
		return
	}
	var pg int
	var order []model.CoreSqlOrder

	start, end := lib.Paging(u.Current, u.PageSize)

	model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
		Scopes(
			commom.AccordingToAllOrderType(u.Expr.Type),
			commom.AccordingToAllOrderState(u.Expr.Status),
			commom.AccordingToDatetime(u.Expr.Picker),
			commom.AccordingToText(u.Expr.Text),
		).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: order, Page: pg}))
}
