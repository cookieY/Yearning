package commom

var (
	ERR_LOGIN = Resp{
		Code: 1301,
		Text: "账号/密码错误,请输入正确的账号密码!",
	}

	ERR_REGISTER = Resp{
		Code: 1302,
		Text: "没有开启注册通道！",
	}

	ERR_REQ_BIND = Resp{
		Code: 1310,
		Text: "传参错误！",
	}

	ERR_REQ_FAKE = Resp{
		Code: 1310,
		Text: "非法传参！",
	}
)

// SOAR 错误码 1900-1999

func ERR_SOAR_ALTER_MERGE(err error) Resp {
	return Resp{
		Code: 1901,
		Text: err.Error(),
	}
}

func ERR_COMMON_MESSAGE(err error) Resp {
	return Resp{
		Code: 5555,
		Text: err.Error(),
	}
}