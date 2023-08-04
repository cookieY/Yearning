package lib

import (
	"sync"
)

const Version = "3.1.6 Uranus"

var OrderDelayPool sync.Map // 延迟池 如果为0则不延迟
