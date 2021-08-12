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
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"Yearning-go/src/parser"
	"encoding/json"
	"fmt"
	"time"
)

func DataInit(o *parser.AuditRole, other *model.Other, ldap *model.Ldap, message *model.Message, a *model.PermissionList) {
	c, _ := json.Marshal(o)
	oh, _ := json.Marshal(other)
	l, _ := json.Marshal(ldap)
	m, _ := json.Marshal(message)
	ak, _ := json.Marshal(a)
	group, _ := json.Marshal([]string{"admin"})
	model.DB().Debug().Create(&model.CoreAccount{
		Username:   "admin",
		RealName:   "超级管理员",
		Password:   lib.DjangoEncrypt("Yearning_admin", string(lib.GetRandom())),
		Rule:       "super",
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
	})
}

func Migrate() {
	if !model.DB().HasTable("core_accounts") {
		model.DB().CreateTable(&model.CoreAccount{})
		model.DB().CreateTable(&model.CoreDataSource{})
		model.DB().CreateTable(&model.CoreGlobalConfiguration{})
		model.DB().CreateTable(&model.CoreGrained{})
		model.DB().CreateTable(&model.CoreSqlOrder{})
		model.DB().CreateTable(&model.CoreSqlRecord{})
		model.DB().CreateTable(&model.CoreRollback{})
		model.DB().CreateTable(&model.CoreQueryRecord{})
		model.DB().CreateTable(&model.CoreQueryOrder{})
		model.DB().CreateTable(&model.CoreAutoTask{})
		model.DB().CreateTable(&model.CoreRoleGroup{})
		model.DB().CreateTable(&model.CoreWorkflowTpl{})
		model.DB().AutoMigrate(&model.CoreWorkflowDetail{})
		o := parser.AuditRole{
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
			DDLMultiToSubmit:               false,
			OscAlterForeignKeysMethod:      "rebuild_constraints",
			OscMaxLag:                      1,
			OscChunkTime:                   0.5,
			OscMaxThreadConnected:          25,
			OscMaxThreadRunning:            25,
			OscCriticalThreadConnected:     20,
			OscCriticalThreadRunning:       20,
			OscRecursionMethod:             "processlist",
			OscCheckInterval:               1,
			AllowCreatePartition:           false,
			AllowCreateView:                false,
			AllowSpecialType:               false,
			OscLockWaitTimeout:             60,
			OscSleep:                       0.0,
			OscCheckUniqueKeyChange:        false,
		}

		other := model.Other{
			Limit:            "1000",
			IDC:              []string{"Aliyun", "AWS"},
			Multi:            false,
			Query:            false,
			ExcludeDbList:    []string{},
			InsulateWordList: []string{},
			Register:         false,
			Export:           false,
			ExQueryTime:      60,
			PerOrder:         2,
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
			Auditor:     []string{},
		}
		time.Sleep(2)
		DataInit(&o, &other, &ldap, &message, &a)

		fmt.Println("初始化成功!\n 用户名: admin\n密码:Yearning_admin")
	} else {
		fmt.Println("已初始化过,请不要再次执行")
	}
}

func UpdateData() {
	fmt.Println("检查更新.......")
	model.DB().AutoMigrate(&model.CoreAccount{})
	model.DB().AutoMigrate(&model.CoreDataSource{})
	model.DB().AutoMigrate(&model.CoreGlobalConfiguration{})
	model.DB().AutoMigrate(&model.CoreGrained{})
	model.DB().AutoMigrate(&model.CoreSqlOrder{})
	model.DB().AutoMigrate(&model.CoreSqlRecord{})
	model.DB().AutoMigrate(&model.CoreRollback{})
	model.DB().AutoMigrate(&model.CoreQueryRecord{})
	model.DB().AutoMigrate(&model.CoreQueryOrder{})
	model.DB().AutoMigrate(&model.CoreAutoTask{})
	model.DB().AutoMigrate(&model.CoreRoleGroup{})
	model.DB().AutoMigrate(&model.CoreWorkflowTpl{})
	model.DB().AutoMigrate(&model.CoreWorkflowDetail{})
	model.DB().LogMode(false).Exec("alter table core_auto_tasks change COLUMN base data_base varchar(50) not null")
	model.DB().LogMode(false).Model(&model.CoreAutoTask{}).DropColumn("base")
	fmt.Println("数据已更新!")
}

func DelCol() {
	model.DB().LogMode(false).Model(&model.CoreQueryOrder{}).DropColumn("source")
}

func MargeRuleGroup() {
	fmt.Println("破坏性变更修复…………")
	model.DB().Model(&model.CoreSqlOrder{}).DropColumn("rejected")
	model.DB().Model(&model.CoreGrained{}).DropColumn("permissions")
	model.DB().Model(&model.CoreGrained{}).DropColumn("rule")
	model.DB().Model(model.CoreAccount{}).Where("rule = ?", "perform").Update(&model.CoreAccount{Rule: "admin"})
	model.DB().Model(model.CoreAccount{}).Where("username = ?", "admin").Update(&model.CoreAccount{Rule: "super"})
	ldap := model.Ldap{
		Url:      "",
		User:     "",
		Password: "",
		Type:     "(&(objectClass=organizationalPerson)(sAMAccountName=%s))",
		Sc:       "",
	}
	b, _ := json.Marshal(ldap)
	model.DB().LogMode(false).Model(model.CoreGlobalConfiguration{}).Update(&model.CoreGlobalConfiguration{Ldap: b})
	fmt.Println("修复成功!")
}
