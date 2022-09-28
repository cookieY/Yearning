package roles

import "github.com/cookieY/yee"

func RolesApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:  SuperFetchRoles,
		Post: SuperSaveRoles,
	}
}
