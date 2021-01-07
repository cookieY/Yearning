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
	Handler yee.HandlerFunc
	Payload string
	Rec     *httptest.ResponseRecorder
	Req     *http.Request
	Yee     *yee.Core
}

func (c *Case) Do() *Case {
	c.Req = httptest.NewRequest(c.Method, c.Uri, strings.NewReader(c.Payload))
	c.Req.Header.Set("Content-Type", yee.MIMEApplicationJSON)
	c.Rec = httptest.NewRecorder()
	c.Yee.ServeHTTP(c.Rec, c.Req)
	return c
}

func (c *Case) Get() *Case {
	c.Yee = yee.C()
	c.Yee.GET(SplitUri(c.Uri), c.Handler)
	return c
}

func (c *Case) Post() *Case {
	c.Yee = yee.C()
	c.Yee.POST(c.Uri, c.Handler)
	return c
}

func (c *Case) Put() *Case {
	c.Yee = yee.C()
	c.Yee.PUT(SplitUri(c.Uri), c.Handler)
	return c
}

func (c *Case) Delete() *Case {
	c.Yee = yee.C()
	c.Yee.DELETE(SplitUri(c.Uri), c.Handler)
	return c
}

func SplitUri(uri string) string {
	url := strings.Split(uri, "?")
	return url[0]
}

func (c *Case) Unmarshal(payload interface{}) {
	u, _ := ioutil.ReadAll(c.Rec.Body)
	if err := json.Unmarshal(u, &payload); err != nil {
		log.Fatal(err.Error())
	}
}