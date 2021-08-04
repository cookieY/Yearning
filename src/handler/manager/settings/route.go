package settings

import "github.com/cookieY/yee"

func SettingsApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    SuperFetchSetting,
		Post:   SuperSaveSetting,
		Put:    SuperTestSetting,
		Delete: SuperDelOrder,
	}
}
