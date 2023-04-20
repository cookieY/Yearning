package query

import "github.com/cookieY/yee"

func AuditQueryRestFulAPis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:    AuditQueryOrderProfileFetchApis,
		Get:    AuditQueryOrderApis,
		Delete: QueryDeleteEmptyRecord,
		Post:   QueryHandlerSets,
	}
}
