package roles

import "github.com/cookieY/yee"

func RolesApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:  SuperFetchRoles,
		Post: SuperSaveRoles,
	}
}
