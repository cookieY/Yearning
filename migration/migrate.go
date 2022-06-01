package main

import (
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee/logger"
	"github.com/google/uuid"
	"strconv"
)

type originOther struct {
	Limit       string   `json:"limit"`
	IDC         []string `json:"idc"`
	Query       bool     `json:"query"`
	Register    bool     `json:"register"`
	Export      bool     `json:"export"`
	ExQueryTime int      `json:"ex_query_time"`
}

type originLDAP struct {
	Url      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
	Type     string `json:"type"`
	Sc       string `json:"sc"`
	Ldaps    bool   `json:"ldaps"`
}

func main() {
	model.DbInit("./conf.toml")
	var s []model.CoreDataSource
	model.DB().AutoMigrate(model.CoreDataSource{})
	model.DB().Model(model.CoreDataSource{}).Find(&s)
	for _, i := range s {
		if i.SourceId == "" {
			model.DB().Model(model.CoreDataSource{}).Where("id =?", i.ID).Update(&model.CoreDataSource{SourceId: uuid.New().String()})
		}
	}
	model.DB().Exec("alter table core_query_orders change COLUMN query_per status tinyint(2) not null")
	model.DB().Model(model.CoreQueryOrder{}).DropColumn("id_c")
	model.DB().Model(model.CoreQueryOrder{}).DropColumn("ex_date")
	model.DB().LogMode(true).Exec("alter table core_query_records change COLUMN base_name `schema` varchar(50) not null")
	model.DB().Model(model.CoreAccount{}).DropColumn("rule")
	model.DB().Model(model.CoreSqlOrder{}).DropColumn("percent")
	model.DB().Model(model.CoreSqlOrder{}).DropColumn("current")
	model.DB().Model(model.CoreSqlOrder{}).DropColumn("executor")
	model.DB().LogMode(true).Exec("alter table core_query_orders change COLUMN realname `real_name` varchar(50) not null")
	model.DB().AutoMigrate(model.CoreRoleGroup{})
	var r []model.CoreRoleGroup
	model.DB().Model(model.CoreRoleGroup{}).Find(&r)
	for _, i := range r {
		var px model.PermissionList
		var dml []string
		var ddl []string
		var query []string
		json.Unmarshal(i.Permissions, &px)
		for _, j1 := range px.DMLSource {
			var sdml model.CoreDataSource
			model.DB().Model(model.CoreDataSource{}).Select("source_id").Where("source =?", j1).First(&sdml)
			dml = append(dml, sdml.SourceId)
		}
		for _, j1 := range px.DDLSource {
			var sddl model.CoreDataSource
			model.DB().Model(model.CoreDataSource{}).Select("source_id").Where("source =?", j1).First(&sddl)
			ddl = append(ddl, sddl.SourceId)
		}
		for _, j1 := range px.QuerySource {
			var squery model.CoreDataSource
			model.DB().Model(model.CoreDataSource{}).Select("source_id").Where("source =?", j1).First(&squery)
			query = append(query, squery.SourceId)
		}
		px.QuerySource = query
		px.DMLSource = dml
		px.DDLSource = ddl
		uj, _ := json.Marshal(px)
		model.DB().Model(model.CoreRoleGroup{}).Where("id =?", i.ID).Update(&model.CoreRoleGroup{GroupId: uuid.New().String(), Permissions: uj})
	}

	var p []model.CoreGrained
	model.DB().Model(model.CoreGrained{}).Find(&p)
	for _, i := range p {
		var k []string
		if err := json.Unmarshal(i.Group, &k); err != nil {
			logger.DefaultLogger.Error(err)
		}
		var newGroup []string
		for _, j := range k {
			var s1 model.CoreRoleGroup
			model.DB().Model(model.CoreRoleGroup{}).Where("name =?", j).First(&s1)
			newGroup = append(newGroup, s1.GroupId)
		}
		if newGroup == nil {
			newGroup = []string{}
		}
		n, _ := json.Marshal(newGroup)
		model.DB().Model(model.CoreGrained{}).Where("id =?", i.ID).Update(&model.CoreGrained{Group: n})
	}

	model.DB().AutoMigrate(model.CoreAccount{})
	model.DB().Model(model.CoreAccount{}).Updates(&model.CoreAccount{IsRecorder: 2})

	var conf model.CoreGlobalConfiguration
	model.DB().Model(model.CoreGlobalConfiguration{}).First(&conf)
	var o originOther
	var l originLDAP
	_ = json.Unmarshal(conf.Other, &o)
	_ = json.Unmarshal(conf.Ldap, &l)
	num, _ := strconv.Atoi(o.Limit)
	other := model.Other{
		Limit:       uint64(num),
		IDC:         o.IDC,
		ExQueryTime: o.ExQueryTime,
		Register:    o.Register,
		Export:      o.Export,
		Query:       o.Query,
	}
	ldap := model.Ldap{
		Url:      l.Url,
		User:     l.User,
		Password: l.Password,
		Type:     "(&(objectClass=organizationalPerson)(sAMAccountName=%s))",
		Sc:       l.Sc,
		Ldaps:    l.Ldaps,
	}
	b, _ := json.Marshal(other)
	ld, _ := json.Marshal(ldap)
	model.DB().Model(model.CoreGlobalConfiguration{}).Update(&model.CoreGlobalConfiguration{Other: b, Ldap: ld})
	fmt.Println("迁移完成！")
}
