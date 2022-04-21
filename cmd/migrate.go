package cmd

import (
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/cookieY/yee/logger"
	"github.com/google/uuid"
)

func migrateTools() {
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
			logger.LogCreator().Error(err)
		}
		var new []string
		for _, j := range k {
			var s1 model.CoreRoleGroup
			model.DB().Model(model.CoreRoleGroup{}).Where("name =?", j).First(&s1)
			new = append(new, s1.GroupId)
		}
		n, _ := json.Marshal(new)
		model.DB().Model(model.CoreGrained{}).Where("id =?", i.ID).Update(&model.CoreGrained{Group: n})
	}

	model.DB().AutoMigrate(model.CoreAccount{})
	model.DB().Model(model.CoreAccount{}).Updates(&model.CoreAccount{IsRecorder: 2})
}
