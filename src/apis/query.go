package apis

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/handler/fetch"
	"Yearning-go/src/handler/personal"
	"Yearning-go/src/lib"
	"github.com/cookieY/yee"
	"net/http"
)

func YearningQueryForGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "tables":
		return personal.FetchQueryTableInfo(y)
	case "schema":
		return personal.FetchQueryDatabaseInfo(y)
	case "results":
		return personal.SocketQueryResults(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningQueryForPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "merge":
		return fetch.FetchMergeDDL(y)
	}
	return y.JSON(http.StatusOK, common.ERR_REQ_FAKE)
}

func YearningQueryForPost(y yee.Context) (err error) {
	tp := y.Params("tp")
	user := new(lib.Token).JwtParse(y)
	switch tp {
	case "post":
		return personal.ReferQueryOrder(y, user)
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
