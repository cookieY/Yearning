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

package handle

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/jinzhu/gorm"
	"github.com/labstack/echo/v4"
	"log"
	"net/http"
	"net/url"
)

type fetchdb struct {
	ComputerRoom   string `json:"computer_room"`
	ConnectionName string `json:"connection_name"`
	Valve          bool   `json:"valve"`
	Username       string `json:"username"`
}

type gr struct {
	Page   int                    `json:"page"`
	Data   []model.CoreDataSource `json:"data"`
	Custom []string               `json:"custom"`
}

type adddb struct {
	Source   string
	IDC      string
	Port     int
	Password string
	User     string
	IP       string
	Username string
	IsQuery  int
}

type testconn struct {
	Ip       string
	User     string
	Password string
	Port     int
}

type editDb struct {
	Data adddb
}

func SuperFetchDB(c echo.Context) (err error) {

	var f fetchdb
	var u []model.CoreDataSource
	var pg int
	con := c.QueryParam("con")
	if err := json.Unmarshal([]byte(con), &f); err != nil {
		c.Logger().Error(err.Error())
	}
	start, end := lib.Paging(c.QueryParam("page"), 10)

	if f.Valve {
		model.DB().Model(model.CoreDataSource{}).Where("id_c LIKE ? and source LIKE ?", "%"+fmt.Sprintf("%s", f.ComputerRoom)+"%", "%"+fmt.Sprintf("%s", f.ConnectionName)+"%").Count(&pg)
		model.DB().Model(model.CoreDataSource{}).Where("id_c LIKE ? and source LIKE ?", "%"+fmt.Sprintf("%s", f.ComputerRoom)+"%", "%"+fmt.Sprintf("%s", f.ConnectionName)+"%").Order("id desc").Offset(start).Limit(end).Find(&u)
	} else {
		model.DB().Order("id desc").Offset(start).Limit(end).Find(&u)
		model.DB().Model(model.CoreDataSource{}).Count(&pg)
	}
	for idx := range u {
		u[idx].Password = "***********"
	}

	return c.JSON(http.StatusOK, gr{Page: pg, Data: u, Custom: model.GloOther.IDC})

}

func SuperAddDB(c echo.Context) (err error) {

	var refer model.CoreDataSource

	u := new(adddb)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	model.DB().Where("source =?", u.Source).First(&refer)

	if refer.Source == "" {

		x := lib.Encrypt(u.Password)

		if x != "" {
			model.DB().Create(&model.CoreDataSource{
				IDC:      u.IDC,
				Source:   u.Source,
				Port:     u.Port,
				IP:       u.IP,
				Password: x,
				Username: u.User,
				IsQuery:  u.IsQuery,
			})
			return c.JSON(http.StatusOK, "连接名添加成功！")
		}
		c.Logger().Error("AES秘钥必须为16位！")
		return c.JSON(http.StatusInternalServerError, nil)
	} else {
		return c.JSON(http.StatusOK, "连接名称重复,请更改为其他!")
	}
}

func SuperDeleteDb(c echo.Context) (err error) {

	var g []model.CoreGrained

	tx := model.DB().Begin()

	source := c.Param("source")

	unescape, _ := url.QueryUnescape(source)

	model.DB().Find(&g)

	if er := tx.Where("source =?", unescape).Delete(&model.CoreDataSource{}).Error; er != nil {
		tx.Rollback()
		c.Logger().Error(er.Error)
		return c.JSON(http.StatusInternalServerError, "")
	}
	for _, i := range g {
		var p model.PermissionList
		if err := json.Unmarshal(i.Permissions, &p); err != nil {
			c.Logger().Error(err.Error())
		}
		p.DDLSource = lib.ResearchDel(p.DDLSource, source)
		p.DMLSource = lib.ResearchDel(p.DMLSource, source)
		p.QuerySource = lib.ResearchDel(p.QuerySource, source)
		r, _ := json.Marshal(p)
		if e := tx.Model(&model.CoreGrained{}).Where("id =?", i.ID).Update(model.CoreGrained{Permissions: r}).Error; e != nil {
			tx.Rollback()
			c.Logger().Error(e.Error())
		}
	}
	tx.Commit()
	return c.JSON(http.StatusOK, "数据库信息已删除")
}

func SuperTestDBConnect(c echo.Context) (err error) {

	u := new(testconn)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}

	db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", u.User, u.Password, u.Ip, u.Port))
	defer func() {
		if err := db.Close(); err != nil {
			log.Println(err.Error())
		}
	}()
	if e != nil {
		c.Logger().Error(e.Error())
		return c.JSON(http.StatusOK, "数据库实例连接失败！请检查相关配置是否正确！")
	}
	return c.JSON(http.StatusOK, "数据库实例连接成功！")
}

func SuperModifyDb(c echo.Context) (err error) {
	u := new(editDb)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	x := lib.Encrypt(u.Data.Password)
	model.DB().Model(&model.CoreDataSource{}).Where("source =?", u.Data.Source).Update(&model.CoreDataSource{IP: u.Data.IP, Port: u.Data.Port, Username: u.Data.Username, Password: x})
	return c.JSON(http.StatusOK, "数据源信息已更新!")
}
