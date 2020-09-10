package apis

import (
	"Yearning-go/src/handle"
	"Yearning-go/src/handle/query"
	"github.com/cookieY/yee"
	"net/http"
)

func YearningQueryForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "fetch_table":
		return query.FetchQueryTableInfo(y)
	case "table_info":
		return query.FetchQueryTableStruct(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryForPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "fetch_base":
		return query.FetchQueryDatabaseInfo(y)
	case "status":
		return query.FetchQueryStatus(y)
	case "merge":
		return handle.GeneralMergeDDL(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryForPost(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "refer":
		return query.ReferQueryOrder(y)
	case "results":
		return query.FetchQueryResults(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    YearningQueryForGet,
		Put:    YearningQueryForPut,
		Post:   YearningQueryForPost,
		Delete: query.UndoQueryOrder,
	}
}
