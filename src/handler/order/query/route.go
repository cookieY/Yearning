package query

import "github.com/cookieY/yee"

func AuditQueryRestFulAPis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:    AuditOrRecordQueryOrderFetchApis,
		Delete: QueryDeleteEmptyRecord,
		Post:   QueryHandlerSets,
	}
}
