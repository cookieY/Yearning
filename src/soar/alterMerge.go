/*
 * Copyright 2018 Xiaomi, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Copyright 2019 HenryYee.
//
// Licensed under the AGPL, Version 3.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.gnu.org/licenses/agpl-3.0.en.html
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package soar

import (
	"errors"
	"fmt"
	"github.com/pingcap/parser"
	"github.com/pingcap/parser/ast"
	_ "github.com/pingcap/tidb/types/parser_driver"
	"log"
	"regexp"
	"strings"
)

func MergeAlterTables(sql string) (string, error) {
	alterSQLs := make(map[string][]string)
	var mergedAlterStr string
	var nonSqls string
	// table/column/index name can be quoted in back ticks
	backTicks := "(`[^\\s]*`)"

	alterExp := regexp.MustCompile(`(?i)alter\s*table\s*(` + backTicks + `|([^\s]*))\s*`) // ALTER TABLE
	createIndexExp := regexp.MustCompile(`(?i)create((unique)|(fulltext)|(spatial)|(primary)|(\s*)\s*)((index)|(key))\s*`)
	renameExp := regexp.MustCompile(`(?i)rename\s*table\s*(` + backTicks + `|([^\s]*))\s*`) // RENAME TABLE
	indexNameExp := regexp.MustCompile(`(?i)(` + backTicks + `|([^\s]*))\s*`)
	indexColsExp := regexp.MustCompile(`(?i)(` + backTicks + `|([^\s]*))\s*on\s*(` + backTicks + `|([^\s]*))\s*`)

	p := parser.New()
	stmt, _, err := p.Parse(sql, "", "")
	if err != nil {
		return "", errors.New(err.Error())
	}
	for _, idx := range stmt {
		alterSQL := ""
		dbName := ""
		tableName := ""
		switch n := idx.(type) {
		case *ast.AlterTableStmt:
			sols := strings.Trim(n.Text(), ";")
			tableName = n.Table.Name.L
			dbName = n.Table.Schema.L
			if alterExp.MatchString(sols) {
				alterSQL = fmt.Sprint(alterExp.ReplaceAllString(sols, ""))
			} else if createIndexExp.MatchString(sols) {
				buf := createIndexExp.ReplaceAllString(sols, "")
				idxName := strings.TrimSpace(indexNameExp.FindString(buf))
				buf = indexColsExp.ReplaceAllString(buf, "")
				alterSQL = fmt.Sprint("ADD INDEX", " "+idxName+" ", buf)
			}
		case *ast.RenameTableStmt:
			sols := strings.Trim(n.Text(), ";")
			if alterExp.MatchString(sols) {
				alterSQL = fmt.Sprint(alterExp.ReplaceAllString(sols, ""))
			} else if renameExp.MatchString(sols) {
				alterSQL = fmt.Sprint(alterExp.ReplaceAllString(sols, ""))
			} else {
				log.Printf("rename not match: %v", n.Text())
			}
		default:
			nonSqls += n.Text() + ";\n"
		}
		if alterSQL != "" && tableName != "" && tableName != "dual" {
			if dbName == "" {
				alterSQLs["`"+tableName+"`"] = append(alterSQLs["`"+tableName+"`"], alterSQL)
			} else {
				alterSQLs["`"+dbName+"`.`"+tableName+"`"] = append(alterSQLs["`"+dbName+"`.`"+tableName+"`"], alterSQL)
			}
		}
	}
	for k, v := range alterSQLs {
		mergedAlterStr +=  fmt.Sprintln("ALTER TABLE", k, strings.Join(v, ", "), ";")
	}
	mergedAlterStr += nonSqls
	return mergedAlterStr, nil
}
