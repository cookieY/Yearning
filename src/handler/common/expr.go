package common

import (
	"gorm.io/gorm"
	"reflect"
	std_time "time"
)

const QueryField = "work_id, username, text, backup, date, real_name, `status`, `type`, `delay`, `source`,`id_c`,`data_base`,`table`,`execute_time`,source_id,assigned,current_step,relevant,`file`"

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
		return db.Where("`status` in (?)", []int{1, 3})
	}
}

func AccordingToAllQueryOrderState(state int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch state {
		case 7:
			return db
		default:
			return db.Where("`status` = ?", state)
		}
	}
}

func AccordingToOrderState() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`status` in (?)", []int{1, 4, 0})
	}
}

func AccordingToAllOrderState(state int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch state {
		case 7:
			return db
		default:
			return db.Where("`status` = ?", state)
		}
	}
}

func AccordingToAllOrderType(state int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch state {
		case 2:
			return db
		default:
			return db.Where("`type` = ?", state)
		}
	}
}

func AccordingToAssigned(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`assigned` like ?", "%"+user+"%")
	}
}

func AccordingQueryToAssigned(isRecord bool, username string) func(db *gorm.DB) *gorm.DB {

	return func(db *gorm.DB) *gorm.DB {
		if isRecord {
			return db
		}
		return db.Where("`assigned` like ?", "%"+username+"%")
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

func AccordingToPrincipal(principal string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if principal == "" {
			return db
		}
		return db.Where("principal like ?", "%"+principal+"%")
	}
}

func AccordingToRealName(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if user == "" {
			return db
		}
		return db.Where("real_name like ?", "%"+user+"%")
	}
}

func AccordingToMail(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if user == "" {
			return db
		}
		return db.Where("email like ?", "%"+user+"%")
	}
}

func AccordingToDate(time []string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if reflect.DeepEqual(time, []string{"", ""}) || len(time) != 2 {
			return db
		}

		// 解决基于提交时间查询工单列表失真问题。先将 "2023-05-06T03:00:30.465Z" 解析成 "2023-05-06 03:00"，跟CoreSqlOrder.Date格式保持一致；
		// 前端传往后端的实际时间会被减少8h，在解析完成后，需要加8h，从而与前端实际选择时间范围一致
		for idx,timeStr := range time {
			t,_ := std_time.Parse(std_time.RFC3339,timeStr)
			time[idx] = t.Add(std_time.Hour * 8).Format("2006-01-02 15:04")
		}
		return db.Where("date >= ? AND date <= ?", time[0], time[1])
	}
}

func AccordingToRelevant(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {

		return db.Where("JSON_SEARCH(relevant, 'all', ?) IS NOT NULL", user)
	}
}

func AccordingToUsernameEqual(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if user == "" {
			return db
		}
		return db.Where("username = ?", user)
	}
}

func AccordingToIDEqual(id int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`id` = ?", id)
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

func AccordingToOrderAccurateIDC(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("id_c = ? ", text)
	}
}

func AccordingToOrderIP(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("ip LIKE ? ", "%"+text+"%")
	}
}

func AccordingToOrderSource(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == "" {
			return db
		}
		return db.Where("source LIKE ? ", "%"+text+"%")
	}
}

func AccordingToOrderType(text int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if text == -1 {
			return db
		}
		return db.Where("`is_query` = ?", text)
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

func AccordingToSchemaNotIn(isSchema bool, excludeDbList []string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if len(excludeDbList) == 0 {
			return db
		}
		if isSchema {
			return db.Where("SCHEMA_NAME not in (?)", excludeDbList)
		}
		return db.Where("table_schema not in (?)", excludeDbList)
	}
}
