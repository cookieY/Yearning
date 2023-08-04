package roles

import (
	"Yearning-go/src/handler/common"
	"github.com/cookieY/yee"
	"net/http"
)

func RolesApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Post: SuperCustomRoles,
	}
}

func SuperCustomRoles(c yee.Context) (err error) {
	tp := c.Params("tp")
	switch tp {
	case "global":
		return SuperFetchRoles(c)
	case "global_updated":
		return SuperSaveRoles(c)
	case "list":
		return SuperRolesList(c)
	case "add":
		return SuperRolesAdd(c)
	case "updated":
		return SuperRoleUpdate(c)
	case "delete":
		return SuperRoleDelete(c)
	case "profile":
		return SuperRoleProfile(c)
	}
	return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)

}
