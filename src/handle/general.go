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
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/handle/manage"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"Yearning-go/src/soar"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type fetch struct {
	Source string
	Base   string
	Table  string
	Board  string
}

type cdx struct {
	F []parser.FieldInfo `json:"f"`
	I []parser.IndexInfo `json:"i"`
}

type _testInfo struct {
	Source   string `json:"source"`
	SQL      string `json:"sql"`
	Database string `json:"data_base"`
	IsDML    bool   `json:"is_dml"`
	WorkId   string `json:"work_id"`
}

func GeneralIDC(c yee.Context) (err error) {
	return c.JSON(http.StatusOK, model.GloOther.IDC)

}

func GeneralSource(c yee.Context) (err error) {
	t := c.QueryParam("idc")
	x := c.QueryParam("xxx")
	if t == "undefined" || x == "undefined" {
		return
	}

	unescape, _ := url.QueryUnescape(t)

	var s model.CoreGrained
	var groups []string
	var sList []string
	var source []model.CoreDataSource
	var inter []string
	var queryAuditor []string
	user, _ := lib.JwtParse(c)
	model.DB().Where("username =?", user).First(&s)
	if err := json.Unmarshal(s.Group, &groups); err != nil {
		c.Logger().Error(err.Error())
		return err
	}

	p := lib.MulitUserRuleMarge(groups)

	model.DB().Select("source").Where("id_c =?", unescape).Find(&source)

	if source != nil {
		for _, i := range source {
			sList = append(sList, i.Source)
		}
		if x == "dml" {
			inter = lib.Intersect(p.DMLSource, sList)
		}
		if x == "ddl" {
			inter = lib.Intersect(p.DDLSource, sList)
		}

		if x == "query" {
			inter = lib.Intersect(p.QuerySource, sList)
			queryAuditor = p.Auditor
		}
		if x == "all" {
			inter = sList
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"assigned": queryAuditor, "source": inter, "x": x})
}

func GeneralBase(c yee.Context) (err error) {

	t := c.QueryParam("source")

	var s model.CoreDataSource

	var tpl model.CoreWorkflowTpl

	var mid []string

	if t == "undefined" {
		return
	}

	unescape, _ := url.QueryUnescape(t)

	if model.DB().Where("source =?", unescape).First(&tpl).RecordNotFound() {
		return c.JSON(http.StatusOK, map[string]interface{}{"results": nil, "highlight": nil, "admin": nil})
	}

	model.DB().Where("source =?", unescape).First(&s)

	result, err := commom.ScanDataRows(s, "", "SHOW DATABASES;", "库名", false)

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	if len(model.GloOther.ExcludeDbList) > 0 {
		mid = lib.Intersect(result.Results, model.GloOther.ExcludeDbList)
		result.Results = lib.NonIntersect(mid, result.Results)
	}

	var whoIsAuditor []manage.Tpl

	_ = json.Unmarshal(tpl.Steps, &whoIsAuditor)

	return c.JSON(http.StatusOK, map[string]interface{}{"results": result.Results, "highlight": result.Highlight, "admin": whoIsAuditor[1].Auditor})
}

func GeneralTable(c yee.Context) (err error) {
	u := new(fetch)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var s model.CoreDataSource

	model.DB().Where("source =?", u.Source).First(&s)

	result, err := commom.ScanDataRows(s, u.Base, "SHOW TABLES;", "表名", false)

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"table": result.Results, "highlight": result.Highlight})
}

func GeneralTableInfo(c yee.Context) (err error) {
	u := new(fetch)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var s model.CoreDataSource

	model.DB().Where("source =?", u.Source).First(&s)

	ps := lib.Decrypt(s.Password)
	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%s)/%s?charset=utf8&parseTime=True&loc=Local", s.Username, ps, s.IP, strconv.Itoa(int(s.Port)), u.Base))
	if err != nil {
		c.Logger().Error(err.Error())
		return err
	}

	defer db.Close()

	var rows []parser.FieldInfo
	var idx []parser.IndexInfo

	if err := db.Raw(fmt.Sprintf("SHOW FULL FIELDS FROM `%s`.`%s`", u.Base, u.Table)).Scan(&rows).Error; err != nil {
		c.Logger().Error(err.Error())
	}

	if err := db.Raw(fmt.Sprintf("SHOW INDEX FROM `%s`.`%s`", u.Base, u.Table)).Scan(&idx).Error; err != nil {
		c.Logger().Error(err.Error())
	}
	return c.JSON(http.StatusOK, cdx{I: idx, F: rows})
}

func GeneralSQLTest(c yee.Context) (err error) {
	u := new(_testInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var s model.CoreDataSource
	model.DB().Where("source =?", u.Source).First(&s)
	ps := lib.Decrypt(s.Password)
	y := pb.LibraAuditOrder{
		IsDML:    u.IsDML,
		SQL:      u.SQL,
		DataBase: u.Database,
		Source: &pb.Source{
			Addr:     s.IP,
			User:     s.Username,
			Port:     int32(s.Port),
			Password: ps,
		},
		Execute: false,
		Check:   true,
	}
	record, err := lib.TsClient(&y)
	if err != nil {
		return c.JSON(http.StatusOK, "")
	}
	return c.JSON(http.StatusOK, record)
}

func SuperSQLTest(c yee.Context) (err error) {
	u := new(_testInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var s model.CoreDataSource
	var order model.CoreSqlOrder
	model.DB().Where("work_id =?", u.WorkId).First(&order)
	model.DB().Where("source =?", order.Source).First(&s)
	ps := lib.Decrypt(s.Password)
	y := pb.LibraAuditOrder{
		IsDML:    order.Type == 1,
		SQL:     order.SQL,
		DataBase: order.DataBase,
		Source: &pb.Source{
			Addr:     s.IP,
			User:     s.Username,
			Port:     int32(s.Port),
			Password: ps,
		},
		Execute: false,
		Check:   true,
	}
	record, err := lib.TsClient(&y)
	if err != nil {
		return c.JSON(http.StatusOK, "")
	}
	return c.JSON(http.StatusOK, record)
}

func GeneralOrderDetailList(c yee.Context) (err error) {
	workId := c.QueryParam("workid")
	var record []model.CoreSqlRecord
	var count int
	start, end := lib.Paging(c.QueryParam("page"), 10)
	model.DB().Model(&model.CoreSqlRecord{}).Where("work_id =?", workId).Count(&count).Offset(start).Limit(end).Find(&record)
	return c.JSON(http.StatusOK, map[string]interface{}{"record": record, "count": count})
}

func GeneralOrderDetailRollSQL(c yee.Context) (err error) {
	workId := c.QueryParam("workid")
	start, end := lib.Paging(c.QueryParam("page"), 5)
	var roll []model.CoreRollback
	var count int
	model.DB().Select("`sql`").Model(model.CoreRollback{}).Where("work_id =?", workId).Count(&count).Offset(start).Limit(end).Find(&roll)
	return c.JSON(http.StatusOK, map[string]interface{}{"sql": roll, "count": count})
}

func GeneralFetchUndo(c yee.Context) (err error) {
	u := c.QueryParam("work_id")
	user, _ := lib.JwtParse(c)
	var undo model.CoreSqlOrder
	if model.DB().Where("username =? AND work_id =? AND `status` =? ", user, u, 2).First(&undo).RecordNotFound() {
		return c.JSON(http.StatusOK, "工单状态已更改！无法撤销")
	}
	lib.MessagePush(undo.WorkId, 6, "")
	model.DB().Where("username =? AND work_id =? AND `status` =? ", user, u, 2).Delete(&model.CoreSqlOrder{})
	return c.JSON(http.StatusOK, "工单已撤销！")
}

func GeneralMergeDDL(c yee.Context) (err error) {
	req := new(model.Queryresults)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	m, err := soar.MergeAlterTables(req.Sql)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]interface{}{"err_code": err.Error(), "e": true})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"sols": m, "e": false})
}

func GeneralFetchSQLInfo(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	limit := c.QueryParam("limit")
	var sql model.CoreSqlOrder
	model.DB().Select("`sql`").Where("work_id =?", workId).First(&sql)
	realSQL := sql.SQL
	if limit == "10" {
		tmp := strings.Split(sql.SQL, ";")
		if len(tmp) > 10 {
			realSQL = strings.Join(tmp[:9], "")
		}
	}
	return c.JSON(http.StatusOK, realSQL)
}

func GeneralPostBoard(c yee.Context) (err error) {
	req := new(fetch)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("id =?", 1).Update(&model.CoreGlobalConfiguration{Board: req.Board})
	return c.JSON(http.StatusOK, "公告已保存")
}

func GeneralFetchBoard(c yee.Context) (err error) {
	var k model.CoreGlobalConfiguration
	model.DB().Where("id =?", 1).First(&k)
	return c.JSON(http.StatusOK, k.Board)
}
