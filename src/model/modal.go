package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

type JSON []byte

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

func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

func (j JSON) MarshalJSON() ([]byte, error) {
	if j == nil {
		return []byte("null"), nil
	}
	return j, nil
}

func (j *JSON) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("null point exception")
	}
	*j = append((*j)[0:0], data...)
	return nil
}

func (j *JSON) UnmarshalToJSON(i interface{}) error {
	err := json.Unmarshal(*j, i)
	return err
}

type CoreAccount struct {
	ID            uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Username      string `gorm:"type:varchar(50);not null;index:user_idx" json:"username"`
	Password      string `gorm:"type:varchar(150);not null" json:"password"`
	Department    string `gorm:"type:varchar(50);" json:"department"`
	RealName      string `gorm:"type:varchar(50);" json:"real_name"`
	Email         string `gorm:"type:varchar(50);" json:"email"`
	IsRecorder    uint   `gorm:"type:tinyint(2) not null default 2" json:"is_recorder"`
	QueryPassword string `gorm:"type:varchar(150);not null default ''" json:"query_password"`
}

type CoreGlobalConfiguration struct {
	ID            uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Authorization string `gorm:"type:varchar(50);not null" json:"authorization"`
	Ldap          JSON   `gorm:"type:json;" json:"ldap"`
	Message       JSON   `gorm:"type:json;" json:"message"`
	Other         JSON   `gorm:"type:json;" json:"other"`
	Stmt          uint   `gorm:"type:tinyint(2) not null default 0" json:"stmt"`
	AuditRole     JSON   `gorm:"type:json;" json:"audit_role"`
	Board         string `gorm:"type:longtext" json:"board"`
}

type CoreSqlRecord struct {
	ID        uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId    string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	SQL       string `gorm:"type:longtext;not null" json:"sql"`
	State     string `gorm:"type:varchar(50);not null;" json:"state"`
	Affectrow uint   `gorm:"type:int(50);not null;" json:"affect_row"`
	Time      string `gorm:"type:varchar(50);not null;" json:"time"`
	Error     string `gorm:"type:longtext" json:"error"`
}

type CoreSqlOrder struct {
	ID          uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId      string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	Username    string `gorm:"type:varchar(50);not null;index:query_idx" json:"username"`
	Status      uint   `gorm:"type:tinyint(2);not null;" json:"status"`
	Type        int    `gorm:"type:tinyint(2);not null" json:"type"` // 1 dml  0 ddl
	Backup      uint   `gorm:"type:tinyint(2);not null" json:"backup"`
	IDC         string `gorm:"type:varchar(50);not null" json:"idc"`
	Source      string `gorm:"type:varchar(50);not null" json:"source"`
	SourceId    string `gorm:"type:varchar(200);not null;index:source_idx"  json:"source_id"`
	DataBase    string `gorm:"type:varchar(50);not null" json:"data_base"`
	Table       string `gorm:"type:varchar(50);not null" json:"table"`
	Date        string `gorm:"type:varchar(50);not null" json:"date"`
	SQL         string `gorm:"type:longtext;not null" json:"sql"`
	Text        string `gorm:"type:longtext;not null" json:"text"`
	Assigned    string `gorm:"type:varchar(550);not null" json:"assigned"`
	Delay       string `gorm:"type:varchar(50);not null;default:'none'" json:"delay"`
	RealName    string `gorm:"type:varchar(50);not null" json:"real_name"`
	ExecuteTime string `gorm:"type:varchar(50);" json:"execute_time"`
	Time        string `gorm:"type:varchar(50);not null;" json:"time"`
	CurrentStep int    `gorm:"type:int(50);not null default 1;" json:"current_step"`
	Relevant    JSON   `gorm:"type:json" json:"relevant"`
	OSCInfo     string `gorm:"type:longtext;default ''" json:"osc_info"`
	File        string `gorm:"type:varchar(200);not null;default ''" json:"file"`
}

type CoreRollback struct {
	ID     uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	SQL    string `gorm:"type:longtext;not null" json:"sql"`
}

type CoreDataSource struct {
	ID               uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	IDC              string `gorm:"type:varchar(50);not null" json:"idc"`
	Source           string `gorm:"type:varchar(50);not null" json:"source"`
	IP               string `gorm:"type:varchar(200);not null" json:"ip"`
	Port             int    `gorm:"type:int(10);not null" json:"port"`
	Username         string `gorm:"type:varchar(50);not null" json:"username"`
	Password         string `gorm:"type:varchar(150);not null" json:"password"`
	IsQuery          int    `gorm:"type:tinyint(2);not null" json:"is_query"` // 0写 1读 2读写
	FlowID           int    `gorm:"type:int(100);not null" json:"flow_id"`
	SourceId         string `gorm:"type:varchar(200);not null;index:source_idx"  json:"source_id"`
	ExcludeDbList    string `gorm:"type:varchar(200);not null" json:"exclude_db_list"`
	InsulateWordList string `gorm:"type:varchar(200);not null" json:"insulate_word_list"`
	Principal        string `gorm:"type:varchar(150);not null" json:"principal"`
	CAFile           string `gorm:"type:longtext;default ''" json:"ca_file"`
	Cert             string `gorm:"type:longtext;default ''" json:"cert"`
	KeyFile          string `gorm:"type:longtext;default ''" json:"key_file"`
}

type CoreGrained struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Username string `gorm:"type:varchar(50);not null;index:user_idx" json:"username"`
	Group    JSON   `gorm:"type:json" json:"group"`
}

type CoreRoleGroup struct {
	ID          uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Name        string `gorm:"type:varchar(50);not null" json:"name"`
	Permissions JSON   `gorm:"type:json" json:"permissions"`
	GroupId     string `gorm:"type:varchar(200);not null;index:group_idx"  json:"group_id"`
}

type CoreQueryOrder struct {
	ID           uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId       string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	Username     string `gorm:"type:varchar(50);not null" json:"username"`
	Date         string `gorm:"type:varchar(50);not null" json:"date"`
	ApprovalTime string `gorm:"type:varchar(50);not null" json:"approval_time"`
	Text         string `gorm:"type:longtext;not null" json:"text"`
	Assigned     string `gorm:"type:varchar(50);not null" json:"assigned"`
	RealName     string `gorm:"type:varchar(50);not null" json:"real_name"`
	Export       uint   `gorm:"type:tinyint(2);not null" json:"export"`
	SourceId     string `gorm:"type:varchar(200);not null;index:source_idx"  json:"source_id"`
	Status       int    `gorm:"type:tinyint(2);not null;index:status_idx" json:"status"`
}

type CoreQueryRecord struct {
	ID     uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	SQL    string `gorm:"type:longtext;not null" json:"sql"`
	ExTime int    `gorm:"type:int(10);not null" json:"ex_time"`
	Time   string `gorm:"type:varchar(50);not null" json:"time"`
	Source string `gorm:"type:varchar(50);not null" json:"source"`
	Schema string `gorm:"type:varchar(50);not null" json:"schema"`
}

type CoreAutoTask struct {
	ID        uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Name      string `gorm:"type:varchar(50);not null" json:"name"`
	Source    string `gorm:"type:varchar(50);not null" json:"source"`
	SourceId  string `gorm:"type:varchar(200);not null;index:source_idx"  json:"source_id"`
	DataBase  string `gorm:"type:varchar(50);not null" json:"data_base"`
	Table     string `gorm:"type:varchar(50);not null" json:"table"`
	Tp        int    `gorm:"type:tinyint(2);not null" json:"tp"` // 0 insert 1 update 2delete
	Affectrow uint   `gorm:"type:int(50);not null default 0;" json:"affect_rows"`
	Status    int    `gorm:"type:tinyint(2);not null default 0" json:"status"` // 0 close 1 on
	TaskId    string `gorm:"type:varchar(200);not null;index:task_idx"  json:"task_id"`
	IDC       string `gorm:"type:varchar(50);not null" json:"idc"`
}

type CoreWorkflowTpl struct {
	ID     uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	Source string `gorm:"type:varchar(50);not null;index:source_idx" json:"source"`
	Steps  JSON   `gorm:"type:json" json:"steps"`
}

type CoreWorkflowDetail struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId   string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	Username string `gorm:"type:varchar(50);not null;index:query_idx" json:"username"`
	Time     string `gorm:"type:varchar(50);not null;" json:"time"`
	Action   string `gorm:"type:varchar(550);not null;" json:"action"`
}

type CoreOrderComment struct {
	ID       uint   `gorm:"primary_key;AUTO_INCREMENT" json:"id"`
	WorkId   string `gorm:"type:varchar(50);not null;index:workId_idx" json:"work_id"`
	Username string `gorm:"type:varchar(50);not null;" json:"username"`
	Content  string `gorm:"type:longtext;" json:"content"`
	Time     string `gorm:"type:varchar(50);not null" json:"time"`
}
