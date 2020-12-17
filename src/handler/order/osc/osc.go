package osc

import (
	"Yearning-go/src/handler/commom"
	"github.com/cookieY/yee"
	"net/http"
)

// OscPercent show OSC percent
func OscPercent(c yee.Context) (err error) {
	var k = &OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, commom.SuccessPayload(k.Percent()))
}

// OscKill will kill OSC command
func OscKill(c yee.Context) (err error) {
	var k = OSC{WorkId: c.Params("work_id")}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(k.Kill()))
}
