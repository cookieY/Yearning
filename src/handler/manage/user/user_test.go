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
	"fmt"
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
}

var apis = []test.Case{
	{Method: http.MethodPut, Uri: "/api/v2/manage/user", Handler: SuperFetchUser, Payload: `{"page":1,"find":{"valve":false}}`},
	{Method: http.MethodPut, Uri: "/api/v2/manage/user", Handler: SuperFetchUser, Payload: `{"page":1,"find":{"valve":true,"dept":"DBA","username":"ad"}}`},
	{Method: http.MethodDelete, Uri: "/api/v2/manage/user?user=admin", Handler: SuperDeleteUser},
	{Method: http.MethodGet, Uri: "/api/v2/manage/user?user=admin&tp=depend", Handler: ManageUserFetch},
	{Method: http.MethodGet, Uri: "/api/v2/manage/user?user=admin&tp=group", Handler: ManageUserFetch},
}

func TestSuperFetchUser(t *testing.T) {
	k := assert.New(t)
	var Ref commom.Resp
	apis[0].Put().Do().Unmarshal(&Ref)
	k.NotEqual(nil, Ref.Payload)
	k.Equal(1200, Ref.Code)
	k.Equal(float64(12), Ref.Payload.(map[string]interface{})["page"].(float64))
	apis[1].Do().Unmarshal(&Ref)
	k.Equal(float64(1), Ref.Payload.(map[string]interface{})["page"].(float64))
}

func TestSuperDeleteUser(t *testing.T) {
	k := assert.New(t)
	var Ref commom.Resp
	apis[2].Delete().Do().Unmarshal(&Ref)
	k.Equal(ADMIN_NOT_DELETE, Ref.Text)
	k.Equal(1200, Ref.Code)
}

func TestDelUserDepend(t *testing.T) {
	var Ref commom.Resp
	k := assert.New(t)
	apis[3].Get().Do().Unmarshal(&Ref)
	k.NotEqual(nil, Ref.Payload)
	k.Equal(1200, Ref.Code)
}

func TestFetchUserGroups(t *testing.T) {
	var Ref commom.Resp
	k := assert.New(t)
	apis[4].Get().Do().Unmarshal(&Ref)
	k.NotEqual(nil, Ref.Payload)
	k.Equal(1200, Ref.Code)
	fmt.Println(Ref.Payload)
}

func BenchmarkSuperFetchUser(b *testing.B) {
	y := yee.C()
	y.PUT("/api/v2/manage/user", SuperFetchUser)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPut, "/api/v2/manage/user", strings.NewReader(`{"page":1,"find":{"valve":false}}`))
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkSuperDeleteUser(b *testing.B) {
	y := yee.C()
	y.DELETE("/api/v2/manage/user", SuperDeleteUser)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodDelete, "/api/v2/manage/user?user=admin", nil)
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkFetchUserGroups(b *testing.B) {
	y := yee.C()
	y.GET("/api/v2/manage/user", ManageUserFetch)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/manage/user?user=admin&tp=group", nil)
		req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		y.ServeHTTP(rec, req)
	}
}

func BenchmarkDelUserDepend(b *testing.B) {
	y := yee.C()
	y.GET("/api/v2/manage/user", ManageUserFetch)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/manage/user?user=admin&tp=depend", nil)
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
