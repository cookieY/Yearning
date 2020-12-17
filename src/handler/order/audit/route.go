package audit

import "github.com/cookieY/yee"

func AuditRestFulAPis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Post:   AuditOrderApis,
		Put: AuditOrRecordOrderFetchApis,
	}
}

