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

package record

import (
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func FetchRecord(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	start, end := lib.Paging(u.Page, 20)

	var pg int

	var order []model.CoreSqlOrder

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToOrderState(),
					commom.AccordingToWorkId(u.Find.Text),
				).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		} else {
			model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).
				Scopes(
					commom.AccordingToOrderState(),
					commom.AccordingToWorkId(u.Find.Text),
					commom.AccordingToDatetime(u.Find.Picker),
				).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
		}
	} else {
		model.DB().Model(&model.CoreSqlOrder{}).Select(commom.QueryField).Scopes(
			commom.AccordingToOrderState(),
		).Count(&pg).Order("id desc").Offset(start).Limit(end).Find(&order)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"data": order, "page": pg, "multi": model.GloOther.Multi})
}

func FetchQueryRecord(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}

	start, end := lib.Paging(u.Page, 20)

	var pg int

	var order []model.CoreQueryOrder

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Model(model.CoreQueryOrder{}).Scopes(
				commom.AccordingToWorkId(u.Find.Text),
				commom.AccordingToQueryPer(),
			).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		} else {
			model.DB().Model(model.CoreQueryOrder{}).Scopes(
				commom.AccordingToQueryPer(),
				commom.AccordingToWorkId(u.Find.Text),
				commom.AccordingToDatetime(u.Find.Picker),
			).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
		}
	} else {
		model.DB().Model(model.CoreQueryOrder{}).Scopes(
			commom.AccordingToQueryPer(),
		).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": order, "page": pg})
}

func FetchQueryRecordDetail(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	start, end := lib.Paging(u.Page, 20)
	var detail []model.CoreQueryRecord
	var count int
	model.DB().Model(&model.CoreQueryRecord{}).Where("work_id =?", u.WorkId).Count(&count).Offset(start).Limit(end).Find(&detail)
	return c.JSON(http.StatusOK, map[string]interface{}{"data": detail, "count": count})
}
