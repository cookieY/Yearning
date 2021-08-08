package db

import (
	"github.com/cookieY/yee"
)

func ManageDbApi() yee.RestfulAPI {
	return yee.RestfulAPI{
		Post:   ManageDBCreateOrEdit,
		Delete: SuperDeleteSource,
		Put:    SuperFetchSource,
	}
}
