package query

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/handler/order/audit"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
	"time"
)

func FetchQueryOrder(c yee.Context) (err error) {
	u := new(commom.PageChange)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	t := new(lib.Token).JwtParse(c)
	if c.QueryParam("tp") != "record" {
		t.IsRecord = false
	}
	order := u.GetSQLQueryList(
		commom.AccordingQueryToAssigned(t),
		commom.AccordingToUsername(u.Expr.Username),
		commom.AccordingToRealName(u.Expr.RealName),
		commom.AccordingToDate(u.Expr.Picker),
		commom.AccordingToWorkId(u.Expr.WorkId),
		commom.AccordingToAllQueryOrderState(u.Expr.Status),
	)
	return c.JSON(http.StatusOK, commom.SuccessPayload(order))
}

func FetchQueryRecordProfile(c yee.Context) (err error) {
	u := new(audit.Confirm)
	if err = c.Bind(u); err != nil {
		return
	}
	start, end := lib.Paging(u.Page, 15)
	var detail []model.CoreQueryRecord
	var count int
	model.DB().Model(&model.CoreQueryRecord{}).Where("work_id =?", u.WorkId).Count(&count).Offset(start).Limit(end).Find(&detail)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: detail, Page: count}))
}

func QueryDeleteEmptyRecord(c yee.Context) (err error) {
	var j []model.CoreQueryOrder
	model.DB().Select("work_id").Where("`status` =?", 3).Find(&j)
	for _, i := range j {
		var k model.CoreQueryRecord
		if model.DB().Where("work_id =?", i.WorkId).First(&k).RecordNotFound() {
			model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
		}
	}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_CLEAR))
}

func QueryHandlerSets(c yee.Context) (err error) {
	u := new(commom.QueryOrder)
	var s model.CoreQueryOrder
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	found := !model.DB().Where("work_id=? AND status=?", u.WorkId, 1).First(&s).RecordNotFound()
	switch c.Params("tp") {
	case "agreed":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(&model.CoreQueryOrder{Status: 2, ApprovalTime: time.Now().Format("2006-01-02 15:04")})
			lib.MessagePush(u.WorkId, 8, "")
		}
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_AGREE))
	case "reject":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(&model.CoreQueryOrder{Status: 4})
			lib.MessagePush(u.WorkId, 9, "")
		}
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_REJECT))
	case "undo":
		t := new(lib.Token)
		t.JwtParse(c)
		var order model.CoreQueryOrder
		model.DB().Model(model.CoreQueryOrder{}).Select("work_id").Where("username =?", t.Username).Last(&order)
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", order.WorkId).Update(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_END))
	case "stop":
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_END))
	case "cancel":
		model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{Status: 3})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_ALL_CANCEL))
	default:
		return
	}
}

func AuditOrRecordQueryOrderFetchApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return FetchQueryOrder(c)
	case "profile":
		return FetchQueryRecordProfile(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}
