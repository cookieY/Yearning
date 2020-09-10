package apis

import (
	"Yearning-go/src/handle"
	"Yearning-go/src/handle/manage"
	"Yearning-go/src/handle/order"
	"github.com/cookieY/yee"
	"net/http"
)

func FetchResourceForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "detail":
		return handle.GeneralOrderDetailList(y)
	case "roll":
		return handle.GeneralOrderDetailRollSQL(y)
	case "undo":
		return handle.GeneralFetchUndo(y)
	case "sql":
		return handle.GeneralFetchSQLInfo(y)
	case "perform":
		return order.FetchPerformList(y)
	case "idc":
		return handle.GeneralIDC(y)
	case "source":
		return handle.GeneralSource(y)
	case "base":
		return handle.GeneralBase(y)

	}
	return y.JSON(http.StatusOK, "Illegal")
}

func FetchResourceForPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "table":
		return handle.GeneralTable(y)
	case "table_info":
		return handle.GeneralTableInfo(y)
	case "test":
		return handle.GeneralSQLTest(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func FetchResourceForPost(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "marge":
		return manage.SuperUserRuleMarge(y)
	case "roll_order":
		return order.RollBackSQLOrder(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningFetchApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Post: FetchResourceForPost,
		Get:  FetchResourceForGet,
		Put:  FetchResourceForPut,
	}
}
