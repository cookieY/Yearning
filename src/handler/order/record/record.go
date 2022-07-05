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
	var count count
	model.DB().Model(model.CoreSqlOrder{}).Where("time > ? and type = 1", timeAdd("-2160")).Count(&count.DML)
	model.DB().Model(model.CoreSqlOrder{}).Where("time > ? and type = 0", timeAdd("-2160")).Count(&count.DDL)
	model.DB().Model(model.CoreSqlOrder{}).Select("time, count(*) as c,type").Where("time > ?", timeAdd("-2160")).Group("time,type").Scan(&order)
	return c.JSON(http.StatusOK, common.SuccessPayload(map[string]interface{}{"order": order, "count": count}))
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
			common.AccordingToDatetime(u.Expr.Picker),
			common.AccordingToText(u.Expr.Text),
		)
	return c.JSON(http.StatusOK, u.ToMessage())
}
