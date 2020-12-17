package tpl

import "github.com/cookieY/yee"

func TplRestApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:  GeneralAllSources,
		Post: TplPostSourceTemplate,
		Put:  TplEditSourceTemplateInfo,
	}
}

