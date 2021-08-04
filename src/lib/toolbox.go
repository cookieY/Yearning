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

package lib

import (
	"Yearning-go/src/model"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cookieY/sqlx"
	_ "github.com/go-sql-driver/mysql"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

const  (
	BUF                     = 1<<20 - 1
)

func ResearchDel(s []string, p string) []string {
	for in := 0; in < len(s); in++ {
		if s[in] == p {
			s = append(s[:in], s[in+1:]...)
			in--
		}
	}
	return s
}

func Paging(page interface{}, total int) (start int, end int) {
	var i int
	switch v := page.(type) {
	case string:
		i, _ = strconv.Atoi(v)
	case int:
		i = v
	}
	start = i*total - total
	end = total
	return
}

func Axis() []string {
	var s []string
	currentTime := time.Now()
	for a := 0; a < 7; a++ {
		oldTime := currentTime.AddDate(0, 0, -a)
		s = append(s, oldTime.Format("2006-01-02"))
	}
	return s
}

func GenWorkid() string {
	rand.Seed(time.Now().UnixNano())
	a := rand.Intn(1000)
	c := strconv.Itoa(a)
	now := time.Now()
	return now.Format("20060102150405") + c
}

func Intersect(o, n []string) []string {
	m := make(map[string]int)
	var arr []string
	for _, v := range o {
		m[v]++
	}
	for _, v := range n {
		m[v]++
		if m[v] > 1 {
			arr = append(arr, v)
		}
	}
	return arr
}

func NonIntersect(o, n []string) []string {
	m := make(map[string]int)
	var arr []string
	for _, v := range o {
		m[v]++
	}
	for _, v := range n {
		m[v]++
		if m[v] == 1 {
			arr = append(arr, v)
		}
	}
	return arr
}

func Time2StrDiff(delay string) time.Duration {
	if delay != "none" {
		now := time.Now()
		dt, _ := time.ParseInLocation("2006-01-02 15:04 ", delay, time.Local)
		after := dt.Sub(now)
		if after+1 > 0 {
			return after
		}
	}
	return 0
}

func TimeDifference(t string) bool {
	dt, _ := time.ParseInLocation("2006-01-02 15:04 ", t, time.Local)
	f := dt.Sub(time.Now())
	if math.Abs(f.Minutes()) > float64(model.GloOther.ExQueryTime) && float64(model.GloOther.ExQueryTime) > 0 {
		return true
	}
	return false
}

type Query struct {
	Field []map[string]string
	Data  []map[string]interface{}
}

func (q *Query) QueryRun(source *model.CoreDataSource, deal *QueryDeal) error {

	db, err := sqlx.Connect("mysql", fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4", source.Username, Decrypt(source.Password), source.IP, source.Port, deal.DataBase))

	if err != nil {
		return err
	}

	defer db.Close()

	rows, err := db.Queryx(deal.Sql)

	if err != nil {
		return err
	}

	cols, err := rows.Columns()

	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		results := make(map[string]interface{})
		_ = rows.MapScan(results)
		for key := range results {
			switch r := results[key].(type) {
			case []uint8:
				if len(r) > BUF {
					results[key] = "blob字段无法显示"
				} else {
					switch hex.EncodeToString(r) {
					case "01":
						results[key] = "true"
					case "00":
						results[key] = "false"
					default:
						results[key] = string(r)
					}
					if excludeFieldContext(key, deal) {
						results[key] = "****脱敏字段"
					}
				}
			}
		}
		q.Data = append(q.Data, results)
	}

	ele := removeDuplicateElement(cols)

	for cv := range ele {
		q.Field = append(q.Field, map[string]string{"title": ele[cv], "key": ele[cv], "width": "200"})
	}

	q.Field[0]["fixed"] = "left"

	return nil
}

func excludeFieldContext(field string, req *QueryDeal) bool {
	if len(req.InsulateWordList) > 0 {
		for _, exclude := range req.InsulateWordList {
			if (strings.Contains(strings.ToLower(field), exclude) && strings.Contains(field, ")")) || strings.ToLower(field) == exclude {
				return true
			}
		}
	}
	return false
}

func removeDuplicateElement(addrs []string) []string {
	result := make([]string, 0, len(addrs))
	temp := map[string]struct{}{}
	idx := 0
	for _, item := range addrs {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		} else {
			idx++
			item += fmt.Sprintf("(%v)", idx)
			result = append(result, item)
		}
	}
	return result
}

func JsonStringify(i interface{}) []byte {
	o, _ := json.Marshal(i)
	return o
}

func removeDuplicateElementForRule(addrs []string) []string {
	result := make([]string, 0, len(addrs))
	temp := map[string]struct{}{}
	for _, item := range addrs {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func MultiUserRuleMarge(group []string) model.PermissionList {
	var u model.PermissionList
	for _, i := range group {
		var k model.CoreRoleGroup
		var m1 model.PermissionList
		model.DB().Where("name =?", i).First(&k)
		_ = json.Unmarshal(k.Permissions, &m1)
		u.DDLSource = append(u.DDLSource, m1.DDLSource...)
		u.DMLSource = append(u.DMLSource, m1.DMLSource...)
		u.QuerySource = append(u.QuerySource, m1.QuerySource...)
		u.Auditor = append(u.Auditor, m1.Auditor...)
	}
	u.DDLSource = removeDuplicateElementForRule(u.DDLSource)
	u.DMLSource = removeDuplicateElementForRule(u.DMLSource)
	u.Auditor = removeDuplicateElementForRule(u.Auditor)
	u.QuerySource = removeDuplicateElementForRule(u.QuerySource)
	return u
}

func EmptyGroup() []byte {
	group, _ := json.Marshal([]string{})
	return group
}
