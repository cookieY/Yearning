package commom

import (
	"github.com/jinzhu/gorm"
	"reflect"
)

func AccordingToWorkId(workId string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if workId == "" {
			return db
		}
		return db.Where("work_id like ?", "%"+workId+"%")
	}
}

func AccordingToQueryPer() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`query_per` in (?)", []int{1, 3})
	}
}

func AccordingToAllQueryOrderState(state int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch state {
		case 7:
			return db
		default:
			return db.Where("`query_per` = ?", state)
		}
	}
}

func AccordingToOrderState() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`status` in (?)", []int{1, 4})
	}
}

func AccordingToAllOrderState(state int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch state {
		case 7:
			return db
		default:
			return db.Where("`status` = (?)", state)
		}
	}
}

func AccordingToAssigned(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`assigned` = ?", user)
	}
}

func AccordingToUsername(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if user == "" {
			return db
		}
		return db.Where("username like ?", "%"+user+"%")
	}
}

func AccordingToDatetime(time []string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if reflect.DeepEqual(time, []string{"", ""}) || len(time) != 2 {
			return db
		}
		return db.Where("time >= ? AND time <= ?", time[0], time[1])
	}
}

func AccordingToDate(time []string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if reflect.DeepEqual(time, []string{"", ""}) || len(time) != 2 {
			return db
		}
		return db.Where("date >= ? AND date <= ?", time[0], time[1])
	}
}

func AccordingToRelevant(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {

		return db.Where("JSON_SEARCH(relevant, 'one', ?) IS NOT NULL", user)
	}
}

func AccordingToUsernameEqual(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("username = ?", user)
	}
}

func AccordingToNameEqual(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`name` = ?", user)
	}
}

func AccordingToText(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("text like ?", "%"+text+"%")
	}
}

func AccordingToOrderName(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("`name` like ?", "%"+text+"%")
	}
}

func AccordingToOrderIDC(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("id_c LIKE ? ", "%"+text+"%")
	}
}

func AccordingToOrderSource(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("`source` LIKE ?", "%"+text+"%")
	}
}

func AccordingToOrderDept(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("department LIKE ?", "%"+text+"%")
	}
}

func AccordingToRuleSuperOrAdmin() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("rule in (?)", []string{"admin", "super"})
	}
}

func AccordingToGroupSourceIsQuery(start, end int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("is_query =? or is_query = ?", start, end)
	}
}

func AccordingToGroupNameIsLike(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("`group` like ?", "%"+text+"%")
	}
}
