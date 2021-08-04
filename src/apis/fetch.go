package apis

import (
	"Yearning-go/src/handler/fetch"
	"Yearning-go/src/handler/manager/group"
	"github.com/cookieY/yee"
	"net/http"
)

func FetchResourceForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "detail":
		return fetch.FetchOrderDetailList(y)
	case "roll":
		return fetch.FetchOrderDetailRollSQL(y)
	case "undo":
		return fetch.FetchUndo(y)
	case "sql":
		return fetch.FetchSQLInfo(y)
	case "perform":
		return fetch.FetchPerformList(y)
	case "idc":
		return fetch.FetchIDC(y)
	case "source":
		return fetch.FetchSource(y)
	case "base":
		return fetch.FetchBase(y)
	case "table":
		return fetch.FetchTable(y)
	case "fields":
		return fetch.FetchTableInfo(y)
	case "steps":
		return fetch.FetchStepsProfile(y)
	case "board":
		return fetch.FetchBoard(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func FetchResourceForPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "test":
		return fetch.FetchSQLTest(y)
	case "merge":
		return fetch.FetchMergeDDL(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func FetchResourceForPost(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "marge":
		return group.SuperUserRuleMarge(y)
	case "roll_order":
		return fetch.RollBackSQLOrder(y)
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
