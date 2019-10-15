package soar

import "testing"

func TestMergeAlterTables(t *testing.T) {
	//sqls := []string{
	//	//"ALTER TABLE T2 ADD COLUMN C int",
	//	//"ALTER TABLE T2 ADD COLUMN D int ",
	//	//"ALTER TABLE T2 ADD COLUMN E int",
	//	//"alter table `t3`add index `idx_a`(a)",
	//	//"alter table`t3`drop index`idx_b`(b)",
	//	//"ALTER TABLE t1 MODIFY col1 BIGINT UNSIGNED DEFAULT 1 COMMENT 'my column';",
	//	//"ALTER TABLE t1 CHANGE  COLUMN c  d;",
	//	//"ALTER TABLE t1 CHANGE COLUMN a  b;",
	//	"ALTER TABLE t2 RENAME INDEX idx_a TO idx_b;",
	//	"ALTER TABLE t2 RENAME KEY idx_a TO idx_b;",
	//	"ALTER TABLE t2 RENAME TO new_table;",
	//}
	a:= "ALTER TABLE t2 RENAME INDEX idx_a TO idx_b;ALTER TABLE t2 RENAME KEY idx_a TO idx_b;ALTER TABLE t2 RENAME TO new_table;ALTER TABLE T3 ADD COLUMN E int;drop table t5"
	MergeAlterTables(a)

}
