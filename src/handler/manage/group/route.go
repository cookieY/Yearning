package group

import "github.com/cookieY/yee"

func GroupsApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    SuperUserRuleMarge,
		Post:   SuperGroupUpdate,
		Put:    SuperGroup,
		Delete: SuperClearUserRule,
	}
}
