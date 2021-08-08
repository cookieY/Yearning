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
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperFetchAutoTaskList(c yee.Context) (err error) {
	u := new(fetchAutoTask)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var task []model.CoreAutoTask
	var pg int
	start, end := lib.Paging(u.Page, 15)
	if u.Find.Valve {
		model.DB().Model(model.CoreAutoTask{}).Scopes(commom.AccordingToOrderName(u.Find.Text)).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&task)
	} else {
		model.DB().Model(model.CoreAutoTask{}).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&task)
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Data: task, Page: pg}))
}

func SuperDeleteAutoTask(c yee.Context) (err error) {
	id := c.QueryParam("id")
	model.DB().Where("id =?", id).Delete(&model.CoreAutoTask{})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_DELETE))
}

func SuperAutoTaskCreateOrEdit(c yee.Context) (err error) {
	u := new(fetchAutoTask)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "create":
		u.Create()
	case "edit":
		u.Edit()
	case "active":
		u.Activation()
	}
	return c.JSON(http.StatusOK,u.Resp)
}
