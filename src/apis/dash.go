package apis

import (
	"Yearning-go/src/handler"
	"github.com/cookieY/yee"
	"net/http"
)

func YearningDashGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "pie":
		return handler.DashPie(y)
	case "axis":
		return handler.DashAxis(y)
	case "count":
		return handler.DashCount(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningDashPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "profile":
		return handler.DashUserInfo(y)
	case "stmt":
		return handler.DashStmt(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningDashApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get: YearningDashGet,
		Put: YearningDashPut,
	}
}
