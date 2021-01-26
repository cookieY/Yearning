package user

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
)

func SuperFetchUser(c yee.Context) (err error) {
	u := new(fetchUser)
	if err := c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var user []model.CoreAccount
	var count int
	start, end := lib.Paging(u.Page, 10)
	if u.Find.Valve {
		model.DB().Model(model.CoreAccount{}).Select(CommonExpr).Scopes(
			commom.AccordingToUsername(u.Find.Username),
			commom.AccordingToOrderDept(u.Find.Dept),
		).Count(&count).Offset(start).Limit(end).Find(&user)
	} else {
		model.DB().Model(model.CoreAccount{}).Select(CommonExpr).Count(&count).Offset(start).Limit(end).Find(&user)
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{
		Page:  count,
		Data:  user,
		Multi: model.GloOther.Multi,
	}))
}

func SuperDeleteUser(c yee.Context) (err error) {
	user := c.QueryParam("user")
	if user == "admin" {
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ADMIN_NOT_DELETE))
	}
	tx := model.DB().Begin()
	model.DB().Where("username =?", user).Delete(&model.CoreAccount{})
	model.DB().Where("username =?", user).Delete(&model.CoreGrained{})
	tx.Commit()

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(USER_DELETE_SUCCESS, user)))
}

func ManageUserCreateOrEdit(c yee.Context) (err error) {
	u := new(CommonUserPost)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "edit":
		return c.JSON(http.StatusOK, SuperUserEdit(&u.User))
	case "create":
		return c.JSON(http.StatusOK, SuperUserRegister(&u.User))
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", u.User.Username).Update(
			model.CoreAccount{Password: lib.DjangoEncrypt(u.User.Password, string(lib.GetRandom()))})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(USER_EDIT_PASSWORD_SUCCESS))

	}
	return c.JSON(http.StatusOK,commom.ERR_REQ_FAKE)
}

func ManageUserFetch(c yee.Context) (err error) {
	u := new(CommonUserGet)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "depend":
		return c.JSON(http.StatusOK, DelUserDepend(u.User))
	case "group":
		var p []model.CoreRoleGroup
		var userP model.CoreGrained
		model.DB().Find(&p)
		model.DB().Where("username=?", u.User).First(&userP)
		return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"group": userP.Group, "list": p}))
	}
	return c.JSON(http.StatusOK,commom.ERR_REQ_FAKE)
}
