package service

import (
	"Yearning-go/src/model"
	"encoding/json"
	"testing"
)

func TestUpdateSoft(t *testing.T) {
	UpdateData()
}
func TestMigrate(t *testing.T) {
	other := model.Other{
		Limit:       1000,
		IDC:         []string{"Aliyun", "AWS"},
		Multi:       false,
		Query:       false,
		Register:    false,
		Export:      false,
		ExQueryTime: 60,
		PerOrder:    2,
	}

	ldap := model.Ldap{
		Url:      "",
		User:     "",
		Password: "",
		Type:     "",
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

	oh, _ := json.Marshal(other)
	l, _ := json.Marshal(ldap)
	m, _ := json.Marshal(message)
	model.DB().Model(model.CoreGlobalConfiguration{}).Update(&model.CoreGlobalConfiguration{
		Ldap:    l,
		Message: m,
		Other:   oh,
	})
}
