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

package db

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
	"net/url"
)

func SuperFetchSource(c yee.Context) (err error) {
	req := new(fetchDB)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	start, end := lib.Paging(req.Page, 10)
	var u []model.CoreDataSource
	var pg int
	if req.Find.Valve {
		model.DB().Model(model.CoreDataSource{}).Scopes(
			commom.AccordingToOrderIDC(req.Find.IDC),
			commom.AccordingToOrderSource(req.Find.Source),
		).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&u)
	} else {
		model.DB().Model(model.CoreDataSource{}).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&u)
	}
	for idx := range u {
		u[idx].Password = "***********"
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Page: pg, Data: u, IDC: model.GloOther.IDC}))
}

func SuperDeleteSource(c yee.Context) (err error) {

	var k []model.CoreRoleGroup

	source := c.QueryParam("source")

	unescape, _ := url.QueryUnescape(source)

	model.DB().Find(&k)

	tx := model.DB().Begin()
	if er := tx.Where("source =?", unescape).Delete(&model.CoreDataSource{}).Error; er != nil {
		tx.Rollback()
		c.Logger().Error(er.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	for i := range k {
		var p model.PermissionList
		if err := json.Unmarshal(k[i].Permissions, &p); err != nil {
			c.Logger().Error(err.Error())
		}
		p.DDLSource = lib.ResearchDel(p.DDLSource, source)
		p.DMLSource = lib.ResearchDel(p.DMLSource, source)
		p.QuerySource = lib.ResearchDel(p.QuerySource, source)
		r, _ := json.Marshal(p)
		if e := tx.Model(&model.CoreRoleGroup{}).Where("id =?", k[i].ID).Update(model.CoreRoleGroup{Permissions: r}).Error; e != nil {
			tx.Rollback()
			c.Logger().Error(e.Error())
		}
	}

	tx.Commit()
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_DELETE))
}

func ManageDBCreateOrEdit(c yee.Context) (err error) {
	u := new(CommonDBPost)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "edit":
		return c.JSON(http.StatusOK, SuperEditSource(&u.DB))
	case "create":
		return c.JSON(http.StatusOK, SuperCreateSource(&u.DB))
	case "test":
		return c.JSON(http.StatusOK, SuperTestDBConnect(&u.DB))
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}
