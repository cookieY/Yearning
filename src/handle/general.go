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
	"Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"Yearning-go/src/soar"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
	ser "github.com/pingcap/parser"
	"net/http"
	"net/url"
	"strconv"
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

type _dbInfo struct {
	results   []string
	highlight []map[string]string
}

func GeneralIDC(c yee.Context) (err error) {
	return c.JSON(http.StatusOK, model.GloOther.IDC)

}

func GeneralSource(c yee.Context) (err error) {
	t := c.Params("idc")
	x := c.Params("xxx")
	if t == "undefined" || x == "undefined" {
		return
	}

	unescape, _ := url.QueryUnescape(t)

	var s model.CoreGrained
	var p model.PermissionList
	var sList []string
	var source []model.CoreDataSource
	var inter []string
	user, _ := lib.JwtParse(c)
	model.DB().Where("username =?", user).First(&s)
	if err := json.Unmarshal(s.Permissions, &p); err != nil {
		c.Logger().Error(err.Error())
		return err
	}

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
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"assigned": p.Auditor, "source": inter, "x": x})
}

func GeneralBase(c yee.Context) (err error) {

	t := c.Params("source")

	var s model.CoreDataSource

	var mid []string

	if t == "undefined" {
		return
	}

	unescape, _ := url.QueryUnescape(t)

	model.DB().Where("source =?", unescape).First(&s)

	result, err := ScanDataRows(s, "", "SHOW DATABASES;","库名")

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	if len(model.GloOther.ExcludeDbList) > 0 {
		mid = lib.Intersect(result.results, model.GloOther.ExcludeDbList)
		result.results = lib.NonIntersect(mid, result.results)
	}
	return c.JSON(http.StatusOK, result.results)
}

func GeneralTable(c yee.Context) (err error) {
	u := new(fetch)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var s model.CoreDataSource

	model.DB().Where("source =?", u.Source).First(&s)

	result, err := ScanDataRows(s, u.Base, "SHOW TABLES;","表名")

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"table": result.results, "highlight": result.highlight})
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
	u := new(ddl)
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

func GeneralOrderDetailList(c yee.Context) (err error) {
	workId := c.QueryParam("workid")
	var record []model.CoreSqlRecord
	var count int
	start, end := lib.Paging(c.QueryParam("page"), 20)
	model.DB().Model(&model.CoreSqlRecord{}).Where("work_id =?", workId).Count(&count).Offset(start).Limit(end).Find(&record)
	return c.JSON(http.StatusOK, struct {
		Record []model.CoreSqlRecord `json:"record"`
		Count  int                   `json:"count"`
	}{
		Record: record,
		Count:  count,
	})
}

func GeneralOrderDetailRollSQL(c yee.Context) (err error) {
	workId := c.QueryParam("workid")
	var order model.CoreSqlOrder
	var roll []model.CoreRollback
	model.DB().Where("work_id =?", workId).First(&order)
	model.DB().Select("`sql`").Where("work_id =?", workId).Find(&roll)
	return c.JSON(http.StatusOK,map[string]interface{}{"order":order,"sql":roll})
}

func GeneralFetchMyOrder(c yee.Context) (err error) {
	u := new(f)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)

	var pg int

	var order []model.CoreSqlOrder

	queryField := "work_id, username, text, backup, date, real_name, executor, status, `data_base`, `table`,assigned,rejected,delay,source,id_c"
	whereField := "username = ? AND text LIKE ? "
	dateField := " AND date >= ? AND date <= ?"

	start, end := lib.Paging(u.Page, 20)

	if u.Find.Valve {
		if u.Find.Picker[0] == "" {
			model.DB().Select(queryField).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.Text)+"%").Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreSqlOrder{}).Where(whereField, user, "%"+fmt.Sprintf("%s", u.Find.Text)+"%").Count(&pg)
		} else {
			model.DB().Select(queryField).
				Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.Text)+"%", u.Find.Picker[0], u.Find.Picker[1]).Order("id desc").Offset(start).Limit(end).Find(&order)
			model.DB().Model(&model.CoreSqlOrder{}).Where(whereField+dateField, user, "%"+fmt.Sprintf("%s", u.Find.Text)+"%", u.Find.Picker[0], u.Find.Picker[1]).Count(&pg)
		}
	} else {
		model.DB().Model(&model.CoreSqlOrder{}).Select(queryField).Where("username = ?", user).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&order)
	}

	return c.JSON(http.StatusOK,map[string]interface{}{"data":order,"page":pg,"multi":model.GloOther.Multi})
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
	workId := c.QueryParam("k")
	var sql model.CoreSqlOrder
	var s []map[string]string
	model.DB().Select("`sql`").Where("work_id =?", workId).First(&sql)
	sqlParser := ser.New()
	stmtNodes, _, err := sqlParser.Parse(sql.SQL, "", "")
	for _, i := range stmtNodes {
		s = append(s, map[string]string{"SQL": i.Text()})
	}
	return c.JSON(http.StatusOK, s)
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

func ScanDataRows(s model.CoreDataSource, database, sql, meta string) (res _dbInfo, err error) {

	ps := lib.Decrypt(s.Password)

	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%s)/%s?charset=utf8&parseTime=True&loc=Local", s.Username, ps, s.IP, strconv.Itoa(int(s.Port)), database))

	defer func() {
		_ = db.Close()
	}()

	var _tmp string

	if err != nil {
		return _dbInfo{}, err
	}

	rows, err := db.Raw(sql).Rows()

	if err != nil {
		return _dbInfo{}, err
	}

	for rows.Next() {
		rows.Scan(&_tmp)
		res.results = append(res.results, _tmp)
		res.highlight = append(res.highlight, map[string]string{"vl": _tmp, "meta": meta})
	}
	return res, nil
}
