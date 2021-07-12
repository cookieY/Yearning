package query

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
	"time"
)

func FetchQueryRecord(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	order := u.GetSQLQueryList(
		commom.AccordingToQueryPer(),
		commom.AccordingToWorkId(u.Find.Text),
		commom.AccordingToDate(u.Find.Picker),
	)
	return c.JSON(http.StatusOK, commom.SuccessPayload(order))
}

func FetchQueryOrder(c yee.Context) (err error) {

	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)
	order := u.GetSQLQueryList(
		commom.AccordingToUsername(u.Find.Text),
		commom.AccordingToAssigned(user),
		commom.AccordingToDate(u.Find.Picker),
		commom.AccordingToAllQueryOrderState(u.Find.Status),
	)
	return c.JSON(http.StatusOK, commom.SuccessPayload(order))
}

func FetchQueryRecordProfile(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	start, end := lib.Paging(u.Page, 20)
	var detail []model.CoreQueryRecord
	var count int
	model.DB().Model(&model.CoreQueryRecord{}).Where("work_id =?", u.WorkId).Count(&count).Offset(start).Limit(end).Find(&detail)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: detail, Page: count}))
}

func QueryDeleteEmptyRecord(c yee.Context) (err error) {
	var j []model.CoreQueryOrder
	model.DB().Select("work_id").Where(`query_per =?`, 3).Find(&j)
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
		return c.JSON(http.StatusOK, err.Error())
	}
	found := !model.DB().Where("work_id=? AND query_per=?", u.WorkId, 2).First(&s).RecordNotFound()
	switch u.Tp {
	case "agreed":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 1, "ex_date": time.Now().Format("2006-01-02 15:04")})
			lib.MessagePush(u.WorkId, 8, "")
		}
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_AGREE))
	case "reject":
		if found {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 0})
			lib.MessagePush(u.WorkId, 9, "")
		}
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_REJECT))
	case "stop":
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Update(map[string]interface{}{"query_per": 3})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_ALL_END))
	case "cancel":
		model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{QueryPer: 3})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_ALL_CANCEL))
	default:
		return
	}
}

func AuditOrRecordQueryOrderFetchApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return FetchQueryOrder(c)
	case "record":
		return FetchQueryRecord(c)
	case "profile":
		return FetchQueryRecordProfile(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}
