package commom

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"errors"
	"fmt"
	"github.com/cookieY/yee/logger"
	"github.com/jinzhu/gorm"
	"strings"
)

func ScanDataRows(s model.CoreDataSource, database, sql, meta string, isQuery bool, isLeaf bool) (res _dbInfo, err error) {

	ps := lib.Decrypt(s.Password)
	if ps == "" {
		return res, errors.New("连接失败,密码解析错误！")
	}
	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local", s.Username, ps, s.IP, s.Port, database))

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

	excludeDbList := lib.MapOn(strings.Split(s.ExcludeDbList, ","))

	for rows.Next() {
		_ = rows.Scan(&_tmp)
		if isQuery {
			if len(excludeDbList) > 0 {
				if _, ok := excludeDbList[_tmp]; ok {
					continue
				}
			}
			res.QueryList = append(res.QueryList, map[string]interface{}{"title": _tmp, "key": checkMeta(_tmp, database, meta), "meta": meta, "isLeaf": isLeaf})
		} else {
			res.Results = append(res.Results, _tmp)
		}
	}
	return res, nil
}

func checkMeta(s, database, flag string) string {
	if flag == "Table" {
		return fmt.Sprintf("`%s`.`%s`", database, s)
	}
	return s
}

func Highlight(s *model.CoreDataSource) []map[string]string {
	ps := lib.Decrypt(s.Password)
	var list []map[string]string
	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", s.Username, ps, s.IP, s.Port))
	if err != nil {
		logger.DefaultLogger.Error(err)
		return nil
	}

	defer func() {
		_ = db.Close()
	}()

	var highlight string

	excludeDbList := strings.Split(s.ExcludeDbList, ",")

	schema, err := db.Table("information_schema.SCHEMATA").Select("SCHEMA_NAME").Scopes(AccordingToSchemaNotIn(true, excludeDbList)).Group("SCHEMA_NAME").Rows()
	for schema.Next() {
		schema.Scan(&highlight)
		list = append(list, map[string]string{"vl": highlight, "meta": "Schema"})
	}

	tbl, err := db.Table("information_schema.tables").Select("table_name").Scopes(AccordingToSchemaNotIn(false, excludeDbList)).Group("table_name").Rows()
	for tbl.Next() {
		tbl.Scan(&highlight)
		list = append(list, map[string]string{"vl": highlight, "meta": "Table"})
	}
	fields, err := db.Table("information_schema.Columns").Select("COLUMN_NAME").Scopes(AccordingToSchemaNotIn(false, excludeDbList)).Group("COLUMN_NAME").Rows()
	for fields.Next() {
		fields.Scan(&highlight)
		list = append(list, map[string]string{"vl": highlight, "meta": "Fields"})
	}

	return list
}

func SuccessPayload(payload interface{}) Resp {
	return Resp{
		Payload: payload,
		Code:    1200,
	}
}

func SuccessPayLoadToMessage(text string) Resp {
	return Resp{
		Text: text,
		Code: 1200,
	}
}
