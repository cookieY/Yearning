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

package group

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
	"net/url"
	"strings"
)

func SuperGroup(c yee.Context) (err error) {
	var page int
	var roles []model.CoreRoleGroup

	f := new(commom.PageInfo)
	if err = c.Bind(f); err != nil {
		return err
	}
	start, end := lib.Paging(f.Page, 10)
	var source []model.CoreDataSource
	var query []model.CoreDataSource
	var u []model.CoreAccount
	model.DB().Select("source").Scopes(commom.AccordingToGroupSourceIsQuery(0, 2)).Find(&source)
	model.DB().Select("source").Scopes(commom.AccordingToGroupSourceIsQuery(1, 2)).Find(&query)
	model.DB().Select("username").Scopes(commom.AccordingToRuleSuperOrAdmin()).Find(&u)
	if f.Find.Valve {
		model.DB().Model(model.CoreRoleGroup{}).Scopes(commom.AccordingToOrderName(f.Find.Text)).Count(&page).Offset(start).Limit(end).Find(&roles)
	} else {
		model.DB().Model(model.CoreRoleGroup{}).Count(&page).Offset(start).Limit(end).Find(&roles)
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(
		commom.CommonList{
			Page:    page,
			Data:    roles,
			Source:  source,
			Query:   query,
			Auditor: u,
		},
	))
}

func SuperGroupUpdate(c yee.Context) (err error) {
	user, _ := lib.JwtParse(c)
	if user == "admin" {
		u := new(k)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
		}
		g, err := json.Marshal(u.Permission)
		if err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
		}
		if u.Tp == 1 {
			var s model.CoreRoleGroup
			if model.DB().Scopes(commom.AccordingToNameEqual(u.Username)).First(&s).RecordNotFound() {
				model.DB().Create(&model.CoreRoleGroup{
					Name:        u.Username,
					Permissions: g,
				})
			} else {
				model.DB().Model(model.CoreRoleGroup{}).Scopes(commom.AccordingToNameEqual(u.Username)).Update(&model.CoreRoleGroup{Permissions: g})
			}
			return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(GROUP_CREATE_SUCCESS, u.Username)))
		} else {
			g, _ := json.Marshal(u.Group)
			model.DB().Model(model.CoreGrained{}).Scopes(commom.AccordingToUsernameEqual(u.Username)).Updates(model.CoreGrained{Group: g})
			return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(GROUP_EDIT_SUCCESS, u.Username)))
		}
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}

func SuperClearUserRule(c yee.Context) (err error) {
	gx := c.QueryParam("clear")
	g, _ := url.QueryUnescape(gx)
	var j []model.CoreGrained
	var k model.CoreRoleGroup
	w := "%" + g + "%"
	model.DB().Where("`group` like ?", w).Find(&j)
	model.DB().Where("`name` =?", gx).First(&k)
	var m2 model.PermissionList
	_ = json.Unmarshal(k.Permissions, &m2)
	for _, i := range j {
		var m1 []string
		_ = json.Unmarshal(i.Group, &m1)
		new := lib.ResearchDel(m1, g)
		per := lib.MultiUserRuleMarge(new)
		s_new, _ := json.Marshal(new)
		s_per, _ := json.Marshal(per)
		model.DB().Model(model.CoreGrained{}).Where("username =?", i.Username).Update(map[string]interface{}{"group": s_new, "permissions": s_per})
	}
	model.DB().Model(model.CoreRoleGroup{}).Where("`name` =?", g).Delete(&model.CoreRoleGroup{})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(GROUP_DELETE_SUCCESS, g)))
}

func SuperUserRuleMarge(c yee.Context) (err error) {
	u := new(marge)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	m3 := lib.MultiUserRuleMarge(strings.Split(u.Group, ","))
	return c.JSON(http.StatusOK, commom.SuccessPayload(m3))
}
