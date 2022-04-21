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

package handler

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

type groupBy struct {
	Source string `json:"source"`
	C      int    `json:"count"`
	Time   string `json:"time"`
	Type   string `json:"type"`
}

type bannerCount struct {
	User      int `json:"user"`
	Order     int `json:"order"`
	Query     int `json:"query"`
	Source    int `json:"source"`
	SelfDDL   int `json:"self_ddl"`
	SelfDML   int `json:"self_dml"`
	SelfAudit int `json:"self_audit"`
	SelfQuery int `json:"self_query"`
}

func DashBanner(c yee.Context) (err error) {
	var b bannerCount
	user := new(lib.Token).JwtParse(c)
	model.DB().Model(model.CoreAccount{}).Count(&b.User)
	model.DB().Model(model.CoreQueryOrder{}).Count(&b.Query)
	model.DB().Model(model.CoreSqlOrder{}).Count(&b.Order)
	model.DB().Model(model.CoreDataSource{}).Count(&b.Source)
	model.DB().Model(model.CoreSqlOrder{}).Where("username =? and `type` =?", user.Username, 0).Count(&b.SelfDDL)
	model.DB().Model(model.CoreSqlOrder{}).Where("username =? and `type` =?", user.Username, 1).Count(&b.SelfDML)
	model.DB().Model(model.CoreQueryOrder{}).Where("username =?", user.Username).Count(&b.SelfQuery)
	model.DB().Model(model.CoreSqlOrder{}).Where("status = ? and assigned like ?", 2, "%"+user.Username+"%").Count(&b.SelfAudit)
	return c.JSON(http.StatusOK, commom.SuccessPayload(b))
}

func DashUserInfo(c yee.Context) (err error) {
	user := new(lib.Token).JwtParse(c)
	var (
		//u         model.CoreAccount
		p         model.CoreGrained
		groupList []model.CoreRoleGroup
		//s         model.CoreGlobalConfiguration
	)
	//model.DB().Select("username,rule,department,real_name,email").Where("username =?", user).Find(&u)
	model.DB().Select("`group`").Where("username =?", user).First(&p)
	model.DB().Select("`name`").Find(&groupList)
	//model.DB().Select("stmt").First(&s)
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"p": p.Group, "g": groupList}))
}

func DashStmt(c yee.Context) (err error) {
	model.DB().Model(&model.CoreGlobalConfiguration{}).Where("authorization =?", "global").Update("stmt", 1)
	return c.JSON(http.StatusOK, nil)
}

func DashTop(c yee.Context) (err error) {
	var source []groupBy
	model.DB().Model(model.CoreSqlOrder{}).Select("source, count(*) as c").Group("source").Order("c desc").Limit(10).Scan(&source)
	return c.JSON(http.StatusOK, commom.SuccessPayload(source))
}
