package autoTask

import "github.com/cookieY/yee"

func SuperAutoTaskApis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:    SuperFetchAutoTaskList,
		Post:   SuperAutoTaskCreateOrEdit,
		Delete: SuperDeleteAutoTask,
	}
}
