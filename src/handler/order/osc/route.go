package osc

import "github.com/cookieY/yee"

func AuditOSCFetchStateApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    OscPercent,
		Delete: OscKill,
	}
}
