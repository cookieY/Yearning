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

package post

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
	"time"
)

func SQLReferToOrder(c yee.Context) (err error) {
	u := new(model.CoreSqlOrder)
	user, _ := lib.JwtParse(c)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}

	w := lib.GenWorkid()
	u.WorkId = w
	u.Username = user
	u.Date = time.Now().Format("2006-01-02 15:04")
	u.Status = 2
	u.Time = time.Now().Format("2006-01-02")
	u.CurrentStep = 1
	u.Relevant = lib.JsonStringify([]string{u.Assigned})

	model.DB().Create(u)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   w,
		Username: user,
		Action:   "已提交",
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
	})

	lib.MessagePush(w, 2, "")

	CallAutoTask(u, w, c)

	return c.JSON(http.StatusOK, "工单已提交,请等待审核人审核！")
}
