package user

import "github.com/cookieY/yee"

func SuperUserApi() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:    SuperFetchUser,
		Post:   ManageUserCreateOrEdit,
		Delete: SuperDeleteUser,
		Get:    ManageUserFetch,
	}
}
