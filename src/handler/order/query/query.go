package query

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/handler/order/audit"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"errors"
	"github.com/cookieY/yee"
	"gorm.io/gorm"
	"net/http"
	"time"
)

func FetchQueryOrder(c yee.Context) (err error) {
	u := new(common.PageList[[]model.CoreQueryOrder])
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	t := new(lib.Token).JwtParse(c)
	if c.QueryParam("tp") != "record" {
		t.IsRecord = false
	}
	u.Paging().Query(
		common.AccordingQueryToAssigned(t),
		common.AccordingToUsername(u.Expr.Username),
		common.AccordingToRealName(u.Expr.RealName),
		common.AccordingToDate(u.Expr.Picker),
		common.AccordingToWorkId(u.Expr.WorkId),
		common.AccordingToAllQueryOrderState(u.Expr.Status),
	)
	return c.JSON(http.StatusOK, u.ToMessage())
}

func FetchQueryRecordProfile(c yee.Context) (err error) {
	u := new(audit.Confirm)
	if err = c.Bind(u); err != nil {
		return
	}
	start, end := lib.Paging(u.Page, 15)
	l := new(common.GeneralList[[]model.CoreQueryRecord])
	model.DB().Model(&model.CoreQueryRecord{}).Where("work_id =?", u.WorkId).Count(&l.Page).Offset(start).Limit(end).Find(&l.Data)
	return c.JSON(http.StatusOK, l.ToMessage())
}

func QueryDeleteEmptyRecord(c yee.Context) (err error) {
	var j []model.CoreQueryOrder
	model.DB().Select("work_id").Where("`status` =?", 3).Find(&j)
	for _, i := range j {
		var k model.CoreQueryRecord
		if err := model.DB().Where("work_id =?", i.WorkId).First(&k).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
		}
	}
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_CLEAR))
}

func QueryHandlerSets(c yee.Context) (err error) {
	u := new(common.QueryOrder)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	found := model.DB().Where("work_id=? AND status=?", u.WorkId, 1).Error
	switch c.Params("tp") {
	case "agreed":
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreQueryOrder{Status: 2, ApprovalTime: time.Now().Format("2006-01-02 15:04")})
			lib.MessagePush(u.WorkId, 8, "")
		}
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_AGREE))
	case "reject":
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreQueryOrder{Status: 4})
			lib.MessagePush(u.WorkId, 9, "")
		}
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_REJECT))
	case "undo":
		t := new(lib.Token)
		t.JwtParse(c)
		var order model.CoreQueryOrder
		model.DB().Model(model.CoreQueryOrder{}).Select("work_id").Where("username =?", t.Username).Last(&order)
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", order.WorkId).Updates(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_END))
	case "stop":
		model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", u.WorkId).Updates(&model.CoreSqlOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_END))
	case "cancel":
		model.DB().Model(model.CoreQueryOrder{}).Updates(&model.CoreQueryOrder{Status: 3})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(common.ORDER_IS_ALL_CANCEL))
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
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}
}
