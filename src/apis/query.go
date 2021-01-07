package apis

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/handler/fetch"
	"Yearning-go/src/handler/personal"
	"Yearning-go/src/lib"
	"github.com/cookieY/yee"
	"net/http"
)

func YearningQueryForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "fetch_table":
		return personal.FetchQueryTableInfo(y)
	case "table_info":
		return personal.FetchQueryTableStruct(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryForPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "fetch_base":
		return personal.FetchQueryDatabaseInfo(y)
	case "status":
		return personal.FetchQueryStatus(y)
	case "merge":
		return fetch.FetchMergeDDL(y)
	}
	return y.JSON(http.StatusOK,commom.ERR_REQ_FAKE)
}

func YearningQueryForPost(y yee.Context) (err error) {
	tp := y.Params("tp")
	user, _ := lib.JwtParse(y)
	switch tp {
	case "refer":
		return personal.ReferQueryOrder(y,&user)
	case "results":
		return personal.FetchQueryResults(y, &user)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    YearningQueryForGet,
		Put:    YearningQueryForPut,
		Post:   YearningQueryForPost,
		Delete: personal.UndoQueryOrder,
	}
}
