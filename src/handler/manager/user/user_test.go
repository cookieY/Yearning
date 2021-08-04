// Copyright 2021 HenryYee.
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

package user

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/model"
	"Yearning-go/src/test"
	"github.com/cookieY/yee"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func init() {
	model.DbInit("../../../../conf.toml")
	apis.NewTest()
}

var apis = test.Case{
	Method:  http.MethodPost,
	Uri:     "/api/v2/manage/user",
	Handler: SuperUserApi(),
}

func TestFetchUser(t *testing.T) {
	var Ref commom.Resp
	apis.Put(`{"page":1,"find":{"valve":false}}`).Do().Unmarshal(&Ref)
	assert.NotEqual(t, nil, Ref.Payload)
	assert.Equal(t, 1200, Ref.Code)
	assert.Equal(t, float64(12), Ref.Payload.(map[string]interface{})["page"].(float64))

	apis.Put(`{"page":1,"find":{"valve":true,"dept":"DBA","username":"ad"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, float64(1), Ref.Payload.(map[string]interface{})["page"].(float64))

	apis.Put(`1{"page":"0"}`).Do().Unmarshal(&Ref)
	assert.Equal(t, 1310, Ref.Code)
}

func TestSuperDeleteUser(t *testing.T) {
	k := assert.New(t)
	var Ref commom.Resp
	apis.Delete("?user=admin").Do().Unmarshal(&Ref)
	k.Equal(ADMIN_NOT_DELETE, Ref.Text)
	k.Equal(1200, Ref.Code)

	apis.Delete("?user=empty").Do().Unmarshal(&Ref)
	k.NotEqual(ADMIN_NOT_DELETE, Ref.Text)
	k.Equal(1200, Ref.Code)
}

func TestDelUserDependAndGroup(t *testing.T) {
	var Ref commom.Resp

	apis.Get("?user=admin&tp=depend").Do().Unmarshal(&Ref)
	assert.NotEqual(t, nil, Ref.Payload)
	assert.Equal(t, 1200, Ref.Code)

	apis.Get("?user=admin&tp=group").Do().Unmarshal(&Ref)
	assert.NotEqual(t, nil, Ref.Payload)
	assert.Equal(t, 1200, Ref.Code)

	apis.Get("?user=admin&tp=error").Do().Unmarshal(&Ref)
	assert.Equal(t, 1310, Ref.Code)
}

func TestManageUserCreateOrEdit(t *testing.T) {
	var Ref commom.Resp
	apis.Post(`{"tp":"error","user":{"password":"123123Zz","username":"testAdmin"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, 1310, Ref.Code)

	apis.Post(`{"tp":"edit","user":{"real_name":"Henry","username":"admin"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, USER_EDIT_SUCCESS, Ref.Text)

	apis.Post(`{"tp":"password","user":{"password":"123123Zz","username":"admin"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, USER_EDIT_PASSWORD_SUCCESS, Ref.Text)

	apis.Post(`{"tp":"create","user":{"password":"123123Zz","username":"testAdmin"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, USER_REGUSTER_SUCCESS, Ref.Text)

	apis.Post(`{"tp":"create","user":{"password":"123123Zz","username":"testAdmin"}}`).Do().Unmarshal(&Ref)
	assert.Equal(t, ER_USER_REGUSTER, Ref.Text)

	model.DB().Model(model.CoreAccount{}).Where("username = ?", "testAdmin").Delete(&model.CoreAccount{})
}

func BenchmarkSuperFetchUser(b *testing.B) {
	y := yee.C()
	y.PUT("/api/v2/manager/user", SuperFetchUser)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPut, "/api/v2/manager/user", strings.NewReader(`{"page":1,"find":{"valve":false}}`))
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkSuperDeleteUser(b *testing.B) {
	y := yee.C()
	y.DELETE("/api/v2/manager/user", SuperDeleteUser)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodDelete, "/api/v2/manager/user?user=admin", nil)
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkFetchUserGroups(b *testing.B) {
	y := yee.C()
	y.GET("/api/v2/manager/user", ManageUserFetch)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/manager/user?user=admin&tp=group", nil)
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkDelUserDepend(b *testing.B) {
	y := yee.C()
	y.GET("/api/v2/manager/user", ManageUserFetch)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/manager/user?user=admin&tp=depend", nil)
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkUserApis(b *testing.B) {
	runtime.GOMAXPROCS(runtime.NumCPU())
	b.Run("BenchmarkSuperFetchUser", BenchmarkSuperFetchUser)
	b.Run("BenchmarkSuperDeleteUser", BenchmarkSuperDeleteUser)
	b.Run("BenchmarkDelUserDepend", BenchmarkDelUserDepend)
	b.Run("BenchmarkFetchUserGroups", BenchmarkFetchUserGroups)
}

/**
BenchmarkUserApis
BenchmarkUserApis/BenchmarkSuperFetchUser-12         	     228	   4656329 ns/op	 225.19 MB/s	   54706 B/op	     864 allocs/op
BenchmarkUserApis/BenchmarkSuperDeleteUser
BenchmarkUserApis/BenchmarkSuperDeleteUser-12        	  371858	      3262 ns/op	321448.15 MB/s	    6650 B/op	      23 allocs/op
BenchmarkUserApis/BenchmarkDelUserDepend
BenchmarkUserApis/BenchmarkDelUserDepend-12          	     278	   4163309 ns/op	 251.86 MB/s	   30808 B/op	     375 allocs/op
BenchmarkUserApis/BenchmarkFetchUserGroups
BenchmarkUserApis/BenchmarkFetchUserGroups-12        	     368	   2974756 ns/op	 352.49 MB/s	   28491 B/op	     373 allocs/op
**/
