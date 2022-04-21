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
	"github.com/cookieY/yee"
	"net/http"
	"net/url"
)

func SuperFetchSource(c yee.Context) (err error) {
	req := new(commom.PageChange)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	start, end := lib.Paging(req.Current, req.PageSize)
	var u []model.CoreDataSource
	var pg int
	model.DB().Model(model.CoreDataSource{}).Scopes(
		commom.AccordingToOrderIDC(req.Expr.IDC),
		commom.AccordingToOrderIP(req.Expr.IP),
		commom.AccordingToOrderType(req.Expr.IsQuery),
		commom.AccordingToOrderSource(req.Expr.Source),
	).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&u)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{Page: pg, Data: u}))
}

func SuperDeleteSource(c yee.Context) (err error) {
	user := new(lib.Token).JwtParse(c)
	if user.Username != "admin" {
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
	var k []model.CoreRoleGroup
	sourceId := c.QueryParam("source_id")

	unescape, _ := url.QueryUnescape(sourceId)

	model.DB().Find(&k)

	tx := model.DB().Begin()
	if er := tx.Where("source_id =?", unescape).Delete(&model.CoreDataSource{}).Error; er != nil {
		tx.Rollback()
		c.Logger().Error(er.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	for i := range k {
		b, err := lib.MultiArrayRemove(k[i].Permissions, []string{"ddl_source", "dml_source", "query_source"}, unescape)
		if err != nil {
			return c.JSON(http.StatusOK, err)
		}
		if e := tx.Model(&model.CoreRoleGroup{}).Where("id =?", k[i].ID).Update(model.CoreRoleGroup{Permissions: b}).Error; e != nil {
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
		if u.DB.Password != "" && lib.Decrypt(u.DB.Password) != "" {
			u.DB.Password = lib.Decrypt(u.DB.Password)
		}
		return c.JSON(http.StatusOK, SuperTestDBConnect(&u.DB))
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}
