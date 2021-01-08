package test

import (
	"encoding/json"
	"github.com/cookieY/yee"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
)

type Case struct {
	Method  string
	Uri     string
	Handler yee.RestfulAPI
	Rec     *httptest.ResponseRecorder
	Req     *http.Request
	Yee     *yee.Core
}

func (c *Case) Do() *Case {
	c.Req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
	c.Rec = httptest.NewRecorder()
	c.Yee.ServeHTTP(c.Rec, c.Req)
	return c
}

func (c *Case) NewTest() {
	c.Yee = yee.C()
	c.Yee.Restful(c.Uri, c.Handler)
}

func (c *Case) Get(payload string) *Case {
	c.Req = httptest.NewRequest(http.MethodGet, c.Uri+payload, nil)
	return c
}

func (c *Case) Post(payload string) *Case {
	c.Req = httptest.NewRequest(http.MethodPost, c.Uri, strings.NewReader(payload))
	return c
}

func (c *Case) Put(payload string) *Case {
	c.Req = httptest.NewRequest(http.MethodPut, c.Uri, strings.NewReader(payload))
	return c
}

func (c *Case) Delete(payload string) *Case {
	c.Req = httptest.NewRequest(http.MethodDelete, c.Uri+payload, nil)
	return c
}

func (c *Case) Unmarshal(payload interface{}) {
	u, _ := ioutil.ReadAll(c.Rec.Body)
	if err := json.Unmarshal(u, &payload); err != nil {
		log.Fatal(err.Error())
	}
}