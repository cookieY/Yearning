package apis

import (
	"Yearning-go/src/handler/fetch"
	"Yearning-go/src/handler/manage/group"
	"github.com/cookieY/yee"
	"net/http"
)

func FetchResourceForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "userinfo":
		return fetch.FetchUserInfo(y)
	case "order_state":
		return fetch.FetchOrderState(y)
	case "detail":
		return fetch.FetchOrderDetailList(y)
	case "roll":
		return fetch.FetchOrderDetailRollSQL(y)
	case "undo":
		return fetch.FetchUndo(y)
	case "timeline":
		return fetch.FetchAuditSteps(y)
	case "sql":
		return fetch.FetchSQLInfo(y)
	case "idc":
		return fetch.FetchIDC(y)
	case "source":
		return fetch.FetchSource(y)
	case "is_query":
		return fetch.FetchIsQueryAudit(y)
	case "query_status":
		return fetch.FetchQueryStatus(y)
	case "base":
		return fetch.FetchBase(y)
	case "highlight":
		return fetch.FetchHighLight(y)
	case "table":
		return fetch.FetchTable(y)
	case "fields":
		return fetch.FetchTableInfo(y)
	case "steps":
		return fetch.FetchStepsProfile(y)
	case "groups":
		return fetch.FetchUserGroups(y)
	case "board":
		return fetch.FetchBoard(y)
	case "comment":
		return fetch.FetchOrderComment(y)
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
	case "comment":
		return fetch.PostOrderComment(y)
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
