package apis

import (
	"Yearning-go/src/handle"
	"github.com/cookieY/yee"
	"net/http"
)

func YearningDashGet(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "pie":
		return handle.DashPie(y)
	case "axis":
		return handle.DashAxis(y)
	case "count":
		return handle.DashCount(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningDashPut(y yee.Context) (err error) {
	tp := y.Params("tp")
	switch tp {
	case "profile":
		return handle.DashUserInfo(y)
	case "stmt":
		return handle.DashStmt(y)
	}
	return y.JSON(http.StatusOK, "Illegal")
}

func YearningDashApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get: YearningDashGet,
		Put: YearningDashPut,
	}
}
