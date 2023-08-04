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

package service

import (
	"Yearning-go/src/engine"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gookit/gcli/v3/interact"
	"os"
	"time"
)

func DataInit(o *engine.AuditRole, other *model.Other, ldap *model.Ldap, message *model.Message, a *model.PermissionList) {
	c, _ := json.Marshal(o)
	oh, _ := json.Marshal(other)
	l, _ := json.Marshal(ldap)
	m, _ := json.Marshal(message)
	ak, _ := json.Marshal(a)
	sId := uuid.New().String()
	group, _ := json.Marshal([]string{sId})
	model.DB().Debug().Create(&model.CoreAccount{
		Username:   "admin",
		RealName:   "超级管理员",
		Password:   lib.DjangoEncrypt("Yearning_admin", string(lib.GetRandom())),
		Department: "DBA",
		Email:      "",
	})
	model.DB().Debug().Create(&model.CoreGlobalConfiguration{
		Authorization: "global",
		Other:         oh,
		AuditRole:     c,
		Message:       m,
		Ldap:          l,
	})
	model.DB().Debug().Create(&model.CoreGrained{
		Username: "admin",
		Group:    group,
	})
	model.DB().Debug().Create(&model.CoreRoleGroup{
		Name:        "admin",
		Permissions: ak,
		GroupId:     sId,
	})
}

func Migrate() {
	if !model.DB().Migrator().HasTable("core_accounts") {
		if os.Getenv("IS_DOCKER") == "" {
			if !interact.Confirm("是否已将数据库字符集设置为UTF8/UTF8MB4?") {
				return
			}
		}
		_ = model.DB().AutoMigrate(&model.CoreAccount{})
		_ = model.DB().AutoMigrate(&model.CoreDataSource{})
		_ = model.DB().AutoMigrate(&model.CoreGlobalConfiguration{})
		_ = model.DB().AutoMigrate(&model.CoreGrained{})
		_ = model.DB().AutoMigrate(&model.CoreOrderComment{})
		_ = model.DB().AutoMigrate(&model.CoreSqlOrder{})
		_ = model.DB().AutoMigrate(&model.CoreSqlRecord{})
		_ = model.DB().AutoMigrate(&model.CoreRollback{})
		_ = model.DB().AutoMigrate(&model.CoreQueryRecord{})
		_ = model.DB().AutoMigrate(&model.CoreQueryOrder{})
		_ = model.DB().AutoMigrate(&model.CoreAutoTask{})
		_ = model.DB().AutoMigrate(&model.CoreRoleGroup{})
		_ = model.DB().AutoMigrate(&model.CoreWorkflowTpl{})
		_ = model.DB().AutoMigrate(&model.CoreWorkflowDetail{})
		_ = model.DB().AutoMigrate(&model.CoreOrderComment{})
		_ = model.DB().AutoMigrate(&model.CoreRules{})
		_ = model.DB().AutoMigrate(&model.CoreTotalTickets{})
		o := engine.AuditRole{
			DMLInsertColumns:               false,
			DMLMaxInsertRows:               10,
			DMLWhere:                       false,
			DMLOrder:                       false,
			DMLSelect:                      false,
			DDLCheckTableComment:           false,
			DDLCheckColumnNullable:         false,
			DDLCheckColumnDefault:          false,
			DDLEnableAcrossDBRename:        false,
			DDLEnableAutoincrementInit:     false,
			DDLEnableAutoIncrement:         false,
			DDLEnableAutoincrementUnsigned: false,
			DDLEnableDropTable:             false,
			DDLEnableDropDatabase:          false,
			DDLEnableNullIndexName:         false,
			DDLIndexNameSpec:               false,
			DDLMaxKeyParts:                 5,
			DDLMaxKey:                      5,
			DDLMaxCharLength:               10,
			DDLAllowColumnType:             false,
			DDLPrimaryKeyMust:              false,
			MaxTableNameLen:                10,
			MaxAffectRows:                  1000,
			SupportCharset:                 "",
			SupportCollation:               "",
			CheckIdentifier:                false,
			MustHaveColumns:                "",
			DDLMultiToCommit:               false,
			AllowCreatePartition:           false,
			AllowCreateView:                false,
			AllowSpecialType:               false,
			DDLEnablePrimaryKey:            false,
		}

		other := model.Other{
			Limit:       1000,
			IDC:         []string{"Aliyun", "AWS"},
			Query:       false,
			Register:    false,
			Export:      false,
			ExQueryTime: 60,
		}

		ldap := model.Ldap{
			Url:      "",
			User:     "",
			Password: "",
			Type:     "(&(objectClass=organizationalPerson)(sAMAccountName=%s))",
			Sc:       "",
		}

		message := model.Message{
			WebHook:  "",
			Host:     "",
			Port:     25,
			User:     "",
			Password: "",
			ToUser:   "",
			Mail:     false,
			Ding:     false,
			Ssl:      false,
		}

		a := model.PermissionList{
			DDLSource:   []string{},
			DMLSource:   []string{},
			QuerySource: []string{},
		}
		time.Sleep(2)
		DataInit(&o, &other, &ldap, &message, &a)
		fmt.Println(i18n.DefaultLang.Load(i18n.INFO_INITIALIZATION_SUCCESS_USERNAME_PASSWORD_RUN_COMMAND))
	} else {
		fmt.Println(i18n.DefaultLang.Load(i18n.INFO_ALREADY_INITIALIZED))
	}
}

func UpdateData() {
	fmt.Println(i18n.DefaultLang.Load(i18n.INFO_CHECKING_UPDATE))
	_ = model.DB().AutoMigrate(&model.CoreAccount{})
	_ = model.DB().AutoMigrate(&model.CoreDataSource{})
	_ = model.DB().AutoMigrate(&model.CoreGlobalConfiguration{})
	_ = model.DB().AutoMigrate(&model.CoreGrained{})
	_ = model.DB().AutoMigrate(&model.CoreSqlOrder{})
	_ = model.DB().AutoMigrate(&model.CoreSqlRecord{})
	_ = model.DB().AutoMigrate(&model.CoreRollback{})
	_ = model.DB().AutoMigrate(&model.CoreQueryRecord{})
	_ = model.DB().AutoMigrate(&model.CoreQueryOrder{})
	_ = model.DB().AutoMigrate(&model.CoreAutoTask{})
	_ = model.DB().AutoMigrate(&model.CoreRoleGroup{})
	_ = model.DB().AutoMigrate(&model.CoreWorkflowTpl{})
	_ = model.DB().AutoMigrate(&model.CoreWorkflowDetail{})
	_ = model.DB().AutoMigrate(&model.CoreOrderComment{})
	_ = model.DB().AutoMigrate(&model.CoreRules{})
	_ = model.DB().AutoMigrate(&model.CoreTotalTickets{})
	if model.DB().Migrator().HasColumn(&model.CoreAutoTask{}, "base") {
		_ = model.DB().Migrator().RenameColumn(&model.CoreAutoTask{}, "base", "data_base")
	}
	if model.DB().Migrator().HasColumn(&model.CoreSqlOrder{}, "uuid") {
		_ = model.DB().Migrator().DropColumn(&model.CoreSqlOrder{}, "uuid")
	}
	if model.DB().Migrator().HasColumn(&model.CoreWorkflowDetail{}, "rejected") {
		_ = model.DB().Migrator().DropColumn(&model.CoreWorkflowDetail{}, "rejected")
	}
	if model.DB().Migrator().HasColumn(&model.CoreAutoTask{}, "base") {
		_ = model.DB().Migrator().DropColumn(&model.CoreAutoTask{}, "base")
	}
	fmt.Println(i18n.DefaultLang.Load(i18n.INFO_DATA_UPDATED))
}

func DelCol() {
	_ = model.DB().Migrator().DropColumn(&model.CoreQueryOrder{}, "source")
}

func MargeRuleGroup() {
	fmt.Println(i18n.DefaultLang.Load(i18n.INFO_FIX_DESTRUCTIVE_CHANGE))
	_ = model.DB().Migrator().DropColumn(&model.CoreSqlOrder{}, "rejected")
	_ = model.DB().Migrator().DropColumn(&model.CoreGrained{}, "permissions")
	_ = model.DB().Migrator().DropColumn(&model.CoreGrained{}, "rule")
	ldap := model.Ldap{
		Url:      "",
		User:     "",
		Password: "",
		Type:     "(&(objectClass=organizationalPerson)(sAMAccountName=%s))",
		Sc:       "",
	}
	b, _ := json.Marshal(ldap)
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("1=1").Updates(&model.CoreGlobalConfiguration{Ldap: b})
	_ = model.DB().Exec("alter table core_sql_orders modify assigned varchar(550) not null")
	_ = model.DB().Exec("alter table core_workflow_details modify action varchar(550) not null")
	fmt.Println(i18n.DefaultLang.Load(i18n.INFO_FIX_SUCCESS))
}
