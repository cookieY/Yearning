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
	"Yearning-go/src/engine"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	mapset "github.com/deckarep/golang-set/v2"
	_ "github.com/go-sql-driver/mysql"
	"github.com/vmihailenco/msgpack/v5"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
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

func TimeDifference(t string) bool {
	if t == "" {
		return false
	}
	dt, _ := time.ParseInLocation("2006-01-02 15:04 ", t, time.Local)
	source := time.Now()
	if math.Abs(source.Sub(dt).Minutes()) > float64(model.GloOther.ExQueryTime) && float64(model.GloOther.ExQueryTime) > 0 {
		return true
	}
	return false
}

func JsonStringify(i interface{}) []byte {
	o, _ := json.Marshal(i)
	return o
}

func NewMultiUserRuleSet(group []string) (u model.PermissionList) {
	for _, i := range group {
		var k model.CoreRoleGroup
		var m1 model.PermissionList
		model.DB().Where("group_id =?", i).First(&k)
		_ = k.Permissions.UnmarshalToJSON(&m1)
		u.DDLSource = append(u.DDLSource, m1.DDLSource...)
		u.DMLSource = append(u.DMLSource, m1.DMLSource...)
		u.QuerySource = append(u.QuerySource, m1.QuerySource...)
	}
	u.DDLSource = mapset.NewSet[string](u.DDLSource...).ToSlice()
	u.DMLSource = mapset.NewSet[string](u.DMLSource...).ToSlice()
	u.QuerySource = mapset.NewSet[string](u.QuerySource...).ToSlice()
	return u
}

func EmptyGroup() []byte {
	group, _ := json.Marshal([]string{})
	return group
}

func MapOn(l []string) map[string]struct{} {
	mp := make(map[string]struct{})
	for _, i := range l {
		mp[i] = struct{}{}
	}
	return mp
}

func ToJson(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func ToMsg(v interface{}) []byte {
	b, err := msgpack.Marshal(v)
	if err != nil {
		fmt.Println(err)
	}
	return b
}

type SourceControl struct {
	User     string
	Kind     int
	SourceId string
	WorkId   string
}

func (i *SourceControl) Equal() bool {
	var u model.CoreGrained
	var rl []string
	var ord model.CoreSqlOrder
	model.DB().Model(model.CoreSqlOrder{}).Where("work_id =?", i.WorkId).First(&ord)
	if strings.Contains(string(ord.Relevant), i.User) {
		return true
	}
	model.DB().Model(model.CoreGrained{}).Where("username =?", i.User).First(&u)
	_ = u.Group.UnmarshalToJSON(&rl)
	p := NewMultiUserRuleSet(rl)
	switch i.Kind {
	case DDL:
		return mapset.NewSet[string](p.DDLSource...).Contains(i.SourceId)
	case DML:
		return mapset.NewSet[string](p.DMLSource...).Contains(i.SourceId)
	case QUERY:
		return mapset.NewSet[string](p.QuerySource...).Contains(i.SourceId)
	}
	return false
}

func CheckDataSourceRule(ruleId int) (*engine.AuditRole, error) {
	if ruleId != 0 {
		var r model.CoreRules
		var rule engine.AuditRole
		model.DB().Where("id = ?", ruleId).First(&r)
		if err := r.AuditRole.UnmarshalToJSON(&rule); err != nil {
			return nil, err
		}
		return &rule, nil
	}
	return &model.GloRole, nil
}

const (
	DDL = iota
	DML
	QUERY
)
