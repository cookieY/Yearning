package manage

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"net/http"
)

type board struct {
	Board string `json:"board"`
}

func GeneralPostBoard(c yee.Context) (err error) {
	req := new(board)
	if err = c.Bind(req); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("1=1").Updates(&model.CoreGlobalConfiguration{Board: req.Board})
	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.BOARD_MESSAGE_SAVE)))
}

func GeneralGetBoard(c yee.Context) (err error) {
	var board model.CoreGlobalConfiguration
	model.DB().Select("board").First(&board)
	return c.JSON(http.StatusOK, common.SuccessPayload(board.Board))
}
