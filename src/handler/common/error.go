package common

import "Yearning-go/src/i18n"

var (
	ERR_LOGIN = Resp{
		Code: 1301,
		Text: i18n.DefaultLang.Load(i18n.ER_LOGIN),
	}

	ERR_REGISTER = Resp{
		Code: 1302,
		Text: i18n.DefaultLang.Load(i18n.ER_REGISTER),
	}

	ERR_REQ_BIND = Resp{
		Code: 1310,
		Text: i18n.DefaultLang.Load(i18n.ER_REQ_BIND),
	}

	ERR_REQ_FAKE = Resp{
		Code: 1310,
		Text: i18n.DefaultLang.Load(i18n.ER_REQ_FAKE),
	}
	ERR_REQ_PASSWORD_FAKE = Resp{
		Code: 1310,
		Text: i18n.DefaultLang.Load(i18n.ER_REQ_PASSWORD_FAKE),
	}

	ERR_RPC = Resp{
		Code: 1311,
		Text: "RPC call failed！",
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

func ERR_COMMON_TEXT_MESSAGE(err string) Resp {
	return Resp{
		Code: 5555,
		Text: err,
	}
}
