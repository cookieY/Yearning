package tpl

import "github.com/cookieY/yee"

func TplRestApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    TplGetAPis,
		Post:   TplPostSourceTemplate,
		Put:    EditSourceTemplateInfo,
		Delete: DeleteSourceTemplateInfo,
	}
}
