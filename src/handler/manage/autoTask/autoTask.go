// Copyright 2019 HenryYee.
//
// Licensed under the AGPL, Version 3.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.gnu.org/licenses/agpl-3.0.en.html
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package autoTask

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperFetchAutoTaskList(c yee.Context) (err error) {
	u := new(common.PageList[[]model.CoreAutoTask])
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	return c.JSON(http.StatusOK, u.Paging().Query(common.AccordingToOrderName(u.Expr.Text)).ToMessage())
}

func SuperDeleteAutoTask(c yee.Context) (err error) {
	id := c.QueryParam("task_id")
	model.DB().Where("task_id =?", id).Delete(&model.CoreAutoTask{})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_DATA_IS_DELETE)))
}

func SuperAutoTaskCreateOrEdit(c yee.Context) (err error) {
	u := new(fetchAutoTask)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "curd":
		u.CURD()
	case "active":
		u.Activation()
	}
	return c.JSON(http.StatusOK, u.Resp)
}
