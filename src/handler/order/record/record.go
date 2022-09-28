package record

import (
	"Yearning-go/src/handler/common"
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
	DDL int64 `json:"ddl"`
	DML int64 `json:"dml"`
}

func timeAdd(add string) string {
	m, _ := time.ParseDuration(fmt.Sprintf("%sh", add))
	return time.Now().Add(m).Format("2006-01-02")
}

func RecordDashAxis(c yee.Context) (err error) {
	var order []groupBy
	model.DB().Model(model.CoreSqlOrder{}).Select("substring(date,1,10) as `time`, count(*) as c,type").Where("date > ?", timeAdd("-2160")).Group("substring(date,1,10) ,`type`").Scan(&order)
	return c.JSON(http.StatusOK, common.SuccessPayload(order))
}

func RecordOrderList(c yee.Context) (err error) {
	u := new(common.PageList[[]model.CoreSqlOrder])
	if err = c.Bind(u); err != nil {
		return
	}
	u.Paging().Select(common.QueryField).
		Query(
			common.AccordingToAllOrderType(u.Expr.Type),
			common.AccordingToAllOrderState(u.Expr.Status),
			common.AccordingToDate(u.Expr.Picker),
			common.AccordingToText(u.Expr.Text),
		)
	return c.JSON(http.StatusOK, u.ToMessage())
}
