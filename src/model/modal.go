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

package model

import (
	"bytes"
	"database/sql/driver"
	"errors"
)

type JSON []byte

func (j JSON) Value() (driver.Value, error) {
	if j.IsNull() {
		return nil, nil
	}
	return string(j), nil
}

func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	s, ok := value.([]byte)
	if !ok {
		return errors.New("Invalid Scan Source")
	}
	*j = append((*j)[0:0], s...)
	return nil
}

func (m JSON) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

func (m *JSON) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("null point exception")
	}
	*m = append((*m)[0:0], data...)
	return nil
}

func (j JSON) IsNull() bool {
	return len(j) == 0 || string(j) == "null"
}

func (j JSON) Equals(j1 JSON) bool {
	return bytes.Equal([]byte(j), []byte(j1))
}

type CoreAccount struct {
	ID         uint   `gorm:"primary_key;AUTO_INCREMENT";json:"id"`
	Username   string `gorm:"type:varchar(50);not null;index:user_idx";json:"username"`
	Password   string `gorm:"type:varchar(150);not null";json:"password"`
	Rule       string `gorm:"type:varchar(10);not null";json:"rule"`
	Department string `gorm:"type:varchar(50);";json:"department"`
	RealName   string `gorm:"type:varchar(50);";json:"realname"`
	Email      string `gorm:"type:varchar(50);";json:"email"`
}

type CoreGlobalConfiguration struct {
	ID            uint   `gorm:"primary_key;AUTO_INCREMENT";json:"id"`
	Authorization string `gorm:"type:varchar(50);not null";json:"authorization"`
	Ldap          JSON   `gorm:"type:json;";json:"ldap"`
	Message       JSON   `gorm:"type:json;";json:"message"`
	Other         JSON   `gorm:"type:json;";json:"other"`
	Stmt          uint   `gorm:"type:tinyint(2) not null default 0"`
	AuditRole     JSON   `gorm:"type:json;";json:"audit_role"`
}

type CoreSqlRecord struct {
	ID        uint   `gorm:"primary_key;AUTO_INCREMENT";json:"id"`
	WorkId    string `gorm:"type:varchar(50);not null;index:workId_idx"`
	SQL       string `gorm:"type:longtext;not null"`
	State     string `gorm:"type:varchar(50);not null;"`
	Affectrow uint   `gorm:"type:int(50);not null;"`
	Time      string `gorm:"type:varchar(50);not null;"`
	Error     string `gorm:"type:longtext"`
}

type CoreSqlOrder struct {
	ID          uint   `gorm:"primary_key;AUTO_INCREMENT"`
	WorkId      string `gorm:"type:varchar(50);not null;index:workId_idx"`
	Username    string `gorm:"type:varchar(50);not null;index:query_idx"`
	Status      uint   `gorm:"type:tinyint(2);not null;"`
	Type        uint   `gorm:"type:tinyint(2);not null"`
	Backup      uint   `gorm:"type:tinyint(2);not null"`
	IDC         string `gorm:"type:varchar(50);not null"`
	Source      string `gorm:"type:varchar(50);not null"`
	DataBase    string `gorm:"type:varchar(50);not null"`
	Table       string `gorm:"type:varchar(50);not null"`
	Date        string `gorm:"type:varchar(50);not null"`
	SQL         string `gorm:"type:longtext;not null"`
	Text        string `gorm:"type:longtext;not null"`
	Assigned    string `gorm:"type:varchar(50);not null"`
	Delay       string `gorm:"type:varchar(50);not null;default:'none'"`
	Rejected    string `gorm:"type:longtext;"`
	RealName    string `gorm:"type:varchar(50);not null"`
	Executor    string `gorm:"type:varchar(50);"`
	ExecuteTime string `gorm:"type:varchar(50);"`
	Time        string `gorm:"type:varchar(50);not null;"`
	Percent     int    `gorm:"type:int(50);"`
	Current     int    `gorm:"type:int(50);"`
	IsKill      uint   `gorm:"type:tinyint(2);not null;default:'0'"`
}

type CoreRollback struct {
	ID     uint   `gorm:"primary_key;AUTO_INCREMENT"`
	WorkId string `gorm:"type:varchar(50);not null;index:workId_idx"`
	SQL    string `gorm:"type:longtext;not null"`
}

type CoreGroupOrder struct {
	ID          uint   `gorm:"primary_key;AUTO_INCREMENT"`
	WorkId      string `gorm:"type:varchar(50);not null;index:workId_idx"`
	Permissions JSON   `gorm:"type:json"`
	Status      int    `gorm:"type:tinyint(5);not null;default 2"`
	Date        string `gorm:"type:varchar(50);not null;"`
	Username    string `gorm:"type:varchar(50);not null"`
}

type CoreDataSource struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT"`
	IDC      string `gorm:"type:varchar(50);not null"`
	Source   string `gorm:"type:varchar(50);not null"`
	IP       string `gorm:"type:varchar(200);not null"`
	Port     int    `gorm:"type:int(10);not null"`
	Username string `gorm:"type:varchar(50);not null"`
	Password string `gorm:"type:varchar(150);not null"`
	IsQuery  int    `gorm:"type:tinyint(2);not null"` // 0写 1读 2读写
}

type CoreGrained struct {
	ID          uint   `gorm:"primary_key;AUTO_INCREMENT"`
	Username    string `gorm:"type:varchar(50);not null"`
	Rule        string `gorm:"type:varchar(10);not null";json:"rule"`
	Permissions JSON   `gorm:"type:json"`
}

type CoreQueryOrder struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT"`
	WorkId   string `gorm:"type:varchar(50);not null;index:workId_idx"`
	Username string `gorm:"type:varchar(50);not null"`
	Date     string `gorm:"type:varchar(50);not null;index:query_idx"`
	Text     string `gorm:"type:longtext;not null"`
	IDC      string `gorm:"type:varchar(50);not null"`
	Assigned string `gorm:"type:varchar(50);not null"`
	Realname string `gorm:"type:varchar(50);not null"`
	Export   uint   `gorm:"type:tinyint(2);not null"`
	QueryPer int    `gorm:"type:tinyint(2);not null"`
	ExDate   string `gorm:"type:varchar(50);"`
}

type CoreQueryRecord struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT"`
	WorkId   string `gorm:"type:varchar(50);not null;index:workId_idx"`
	SQL      string `gorm:"type:longtext;not null"`
	ExTime   int    `gorm:"type:int(10);not null"`
	Time     string `gorm:"type:varchar(50);not null"`
	Source   string `gorm:"type:varchar(50);not null"`
	BaseName string `gorm:"type:varchar(50);not null"`
}

type CoreAutoTask struct {
	ID        uint   `gorm:"primary_key;AUTO_INCREMENT"`
	Name      string `gorm:"type:varchar(50);not null"`
	Source    string `gorm:"type:varchar(50);not null"`
	Base      string `gorm:"type:varchar(50);not null"`
	Table     string `gorm:"type:varchar(50);not null"`
	Tp        int    `gorm:"type:tinyint(2);not null"` // 0 insert 1 update 2delete
	Affectrow uint   `gorm:"type:int(50);not null default 0;"`
	Status    int    `gorm:"type:tinyint(2);not null default 0"` // 0 close 1 on
}
