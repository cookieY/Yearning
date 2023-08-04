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
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperFetchSource(c yee.Context) (err error) {
	req := new(common.PageList[[]model.CoreDataSource])
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	req.Paging().Query(
		common.AccordingToOrderIDC(req.Expr.IDC),
		common.AccordingToOrderIP(req.Expr.IP),
		common.AccordingToOrderType(req.Expr.IsQuery),
		common.AccordingToOrderSource(req.Expr.Source))
	return c.JSON(http.StatusOK, req.ToMessage())
}

func SuperDeleteSource(c yee.Context) (err error) {
	user := new(lib.Token).JwtParse(c)
	if user.Username != "admin" {
		return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
	}

	var k []model.CoreRoleGroup

	sourceId := c.QueryParam("source_id")

	model.DB().Find(&k)

	tx := model.DB().Begin()
	if er := tx.Where("source_id =?", sourceId).Delete(&model.CoreDataSource{}).Error; er != nil {
		tx.Rollback()
		c.Logger().Error(er.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	for i := range k {
		b, err := lib.MultiArrayRemove(k[i].Permissions, []string{"ddl_source", "dml_source", "query_source"}, sourceId)
		if err != nil {
			return c.JSON(http.StatusOK, err)
		}
		if e := tx.Model(&model.CoreRoleGroup{}).Where("id =?", k[i].ID).Updates(model.CoreRoleGroup{Permissions: b}).Error; e != nil {
			tx.Rollback()
			c.Logger().Error(e.Error())
		}
	}

	tx.Commit()
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_DATA_IS_DELETE)))
}

func ManageDBCreateOrEdit(c yee.Context) (err error) {
	u := new(CommonDBPost)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "edit":
		return c.JSON(http.StatusOK, SuperEditSource(&u.DB))
	case "create":
		return c.JSON(http.StatusOK, SuperCreateSource(&u.DB))
	case "test":
		if u.DB.Password != "" && lib.Decrypt(model.JWT, u.DB.Password) != "" {
			u.DB.Password = lib.Decrypt(model.JWT, u.DB.Password)
		}
		return c.JSON(http.StatusOK, SuperTestDBConnect(&u.DB))
	}
	return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
}
