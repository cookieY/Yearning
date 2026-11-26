// rollback_exec.go Provides in-transaction DML and pool-level rollback forensics helpers (before/after
// row image queries, LAST_INSERT_ID retrieval, PG 18+ RETURNING OLD/NEW scanning, and DML/DDL table metadata snapshots).
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func inspectMySQLTableRollbackMetadata(ctx context.Context, q sqlQueryer, schemaName, tableName string) (*TableRollbackMetadata, error) {
	meta := &TableRollbackMetadata{
		Engine:     "mysql",
		SchemaName: schemaName,
		TableName:  tableName,
	}
	var ver string
	if err := q.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&ver); err != nil {
		return nil, err
	}
	meta.ServerVersion = ver
	meta.ServerVersionNum = parseMySQLVersionNum(ver)

	var tableType string
	var estRows sql.NullInt64
	var createOpts sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT table_type, table_rows, create_options
		FROM information_schema.tables
		WHERE table_schema = ? AND table_name = ?`, schemaName, tableName).
		Scan(&tableType, &estRows, &createOpts)
	if errors.Is(err, sql.ErrNoRows) {
		meta.Exists = false
		return meta, nil
	}
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(tableType, "BASE TABLE") {
		meta.Exists = false
		return meta, nil
	}
	meta.Exists = true
	meta.EstimatedRows = estRows.Int64
	if strings.Contains(strings.ToLower(createOpts.String), "partitioned") {
		meta.IsPartitioned = true
	}

	// Column definitions
	colRows, err := q.QueryContext(ctx, `
		SELECT column_name, ordinal_position, data_type, column_type,
		       is_nullable = 'YES', column_default, column_key, extra,
		       ifnull(generation_expression, ''), ifnull(column_comment, '')
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ?
		ORDER BY ordinal_position`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = colRows.Close() }()
	for colRows.Next() {
		var c RollbackColumnMeta
		var def sql.NullString
		var colKey, extra string
		if err := colRows.Scan(
			&c.Name, &c.Ordinal, &c.DataType, &c.ColumnType,
			&c.Nullable, &def, &colKey, &extra,
			&c.GenerationExpr, &c.Comment,
		); err != nil {
			return nil, err
		}
		if def.Valid {
			val := def.String
			c.DefaultExpr = &val
		}
		lowerExtra := strings.ToLower(extra)
		if strings.Contains(lowerExtra, "auto_increment") {
			c.IsAutoIncrement = true
		}
		if strings.Contains(lowerExtra, "generated") {
			c.IsGenerated = true
		}
		meta.Columns = append(meta.Columns, c)
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	// Indexes and primary key
	idxRows, err := q.QueryContext(ctx, `
		SELECT index_name, non_unique, seq_in_index, column_name, index_type, expression
		FROM information_schema.statistics
		WHERE table_schema = ? AND table_name = ?
		ORDER BY index_name, seq_in_index`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = idxRows.Close() }()
	type idxAcc struct {
		meta       RollbackIndexMeta
		hasExprCol bool
	}
	idxMap := map[string]*idxAcc{}
	var idxOrder []string
	pkPos := map[string]int{}
	for idxRows.Next() {
		var idxName, indexType string
		var nonUnique, seq int
		var colName, expr sql.NullString
		if err := idxRows.Scan(&idxName, &nonUnique, &seq, &colName, &indexType, &expr); err != nil {
			return nil, err
		}
		acc, ok := idxMap[idxName]
		if !ok {
			acc = &idxAcc{
				meta: RollbackIndexMeta{
					Name:      idxName,
					Unique:    nonUnique == 0,
					Primary:   strings.EqualFold(idxName, "PRIMARY"),
					IndexType: indexType,
				},
			}
			idxMap[idxName] = acc
			idxOrder = append(idxOrder, idxName)
		}
		if colName.Valid && colName.String != "" {
			acc.meta.Columns = append(acc.meta.Columns, colName.String)
			if acc.meta.Primary {
				pkPos[colName.String] = seq
			}
		} else {
			acc.hasExprCol = true
		}
	}
	if err := idxRows.Err(); err != nil {
		return nil, err
	}

	colNullable := map[string]bool{}
	for i := range meta.Columns {
		colNullable[strings.ToLower(meta.Columns[i].Name)] = meta.Columns[i].Nullable
		if pos, ok := pkPos[meta.Columns[i].Name]; ok {
			meta.Columns[i].IsPrimaryKey = true
			meta.Columns[i].PKOrdinal = pos
		}
	}

	qSchema, errS := quoteMySQLOpen(schemaName)
	qTable, errT := quoteMySQLOpen(tableName)
	for _, name := range idxOrder {
		acc := idxMap[name]
		if errS == nil && errT == nil && !acc.meta.Primary && len(acc.meta.Columns) > 0 && !acc.hasExprCol {
			qIdx, errI := quoteMySQLOpen(acc.meta.Name)
			if errI == nil {
				quotedCols := make([]string, 0, len(acc.meta.Columns))
				for _, col := range acc.meta.Columns {
					if qc, errC := quoteMySQLOpen(col); errC == nil {
						quotedCols = append(quotedCols, qc)
					}
				}
				if len(quotedCols) == len(acc.meta.Columns) {
					uniqueKw := ""
					if acc.meta.Unique {
						uniqueKw = "UNIQUE "
					}
					acc.meta.CreateDDL = fmt.Sprintf("CREATE %sINDEX %s ON %s.%s (%s)", uniqueKw, qIdx, qSchema, qTable, strings.Join(quotedCols, ", "))
				}
			}
		}
		meta.Indexes = append(meta.Indexes, acc.meta)
		if acc.meta.Primary && !acc.hasExprCol && len(acc.meta.Columns) > 0 {
			meta.HasExplicitPrimaryKey = true
			meta.IdentityKeyColumns = append([]string(nil), acc.meta.Columns...)
		}
	}
	if len(meta.IdentityKeyColumns) == 0 {
		// If there is no PRIMARY KEY, find the first UNIQUE index whose columns are all NOT NULL to serve as the row identity key.
		for _, name := range idxOrder {
			acc := idxMap[name]
			if !acc.meta.Unique || acc.hasExprCol || len(acc.meta.Columns) == 0 {
				continue
			}
			allNotNull := true
			for _, col := range acc.meta.Columns {
				if colNullable[strings.ToLower(col)] {
					allNotNull = false
					break
				}
			}
			if allNotNull {
				meta.IdentityKeyColumns = append([]string(nil), acc.meta.Columns...)
				break
			}
		}
	}

	// Table-level constraints
	conRows, err := q.QueryContext(ctx, `
		SELECT tc.constraint_name, tc.constraint_type, ifnull(cc.check_clause, '')
		FROM information_schema.table_constraints tc
		LEFT JOIN information_schema.check_constraints cc
		  ON cc.constraint_schema = tc.constraint_schema
		 AND cc.constraint_name = tc.constraint_name
		WHERE tc.table_schema = ? AND tc.table_name = ?
		ORDER BY tc.constraint_name`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conRows.Close() }()
	for conRows.Next() {
		var cm RollbackConstraintMeta
		if err := conRows.Scan(&cm.Name, &cm.Type, &cm.Definition); err != nil {
			return nil, err
		}
		if idx, ok := idxMap[cm.Name]; ok {
			cm.Columns = append([]string(nil), idx.meta.Columns...)
		}
		meta.Constraints = append(meta.Constraints, cm)
	}
	if err := conRows.Err(); err != nil {
		return nil, err
	}

	// Trigger count
	_ = q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.triggers
		WHERE event_object_schema = ? AND event_object_table = ?`,
		schemaName, tableName).Scan(&meta.TriggerCount)

	// Child-table foreign key cascade rules referencing this table
	fkRows, err := q.QueryContext(ctx, `
		SELECT kcu.table_schema, kcu.table_name, rc.constraint_name, rc.update_rule, rc.delete_rule
		FROM information_schema.referential_constraints rc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_schema = rc.constraint_schema
		 AND kcu.constraint_name = rc.constraint_name
		 AND kcu.ordinal_position = 1
		WHERE rc.unique_constraint_schema = ? AND rc.referenced_table_name = ?`,
		schemaName, tableName)
	if err == nil {
		defer func() { _ = fkRows.Close() }()
		for fkRows.Next() {
			var fk RollbackForeignKeyMeta
			if err := fkRows.Scan(&fk.ChildSchema, &fk.ChildTable, &fk.ConstraintName, &fk.UpdateRule, &fk.DeleteRule); err == nil {
				meta.IncomingForeignKeys = append(meta.IncomingForeignKeys, fk)
			}
		}
	}

	// SHOW CREATE TABLE
	if errS == nil && errT == nil {
		var tbl, createSQL string
		if err := q.QueryRowContext(ctx, fmt.Sprintf("SHOW CREATE TABLE %s.%s", qSchema, qTable)).Scan(&tbl, &createSQL); err == nil {
			meta.CreateTableDDL = createSQL
		}
	}
	return meta, nil
}

func inspectPostgresTableRollbackMetadata(ctx context.Context, q sqlQueryer, schemaName, tableName string, allowFallback bool) (*TableRollbackMetadata, error) {
	if schemaName == "" && !allowFallback {
		var curSchema sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&curSchema); err == nil && curSchema.Valid {
			schemaName = curSchema.String
		}
	}
	meta := &TableRollbackMetadata{
		Engine:     "postgresql",
		SchemaName: schemaName,
		TableName:  tableName,
	}
	if err := q.QueryRowContext(ctx,
		`SELECT current_setting('server_version_num')::int, current_setting('server_version')`,
	).Scan(&meta.ServerVersionNum, &meta.ServerVersion); err != nil {
		return nil, err
	}

	var relOID uint32
	var relKind string
	var relTuples float64
	err := q.QueryRowContext(ctx, `
		SELECT c.oid::bigint, c.relkind::text, CASE WHEN c.reltuples < 0 THEN 0 ELSE c.reltuples END
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`,
		schemaName, tableName).Scan(&relOID, &relKind, &relTuples)
	if errors.Is(err, sql.ErrNoRows) && allowFallback {
		var resolvedSchema string
		err = q.QueryRowContext(ctx, `
			SELECT c.oid::bigint, n.nspname, c.relkind::text, CASE WHEN c.reltuples < 0 THEN 0 ELSE c.reltuples END
			FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = $1 AND pg_catalog.pg_table_is_visible(c.oid)`,
			tableName).Scan(&relOID, &resolvedSchema, &relKind, &relTuples)
		if err == nil && resolvedSchema != "" {
			meta.SchemaName = resolvedSchema
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		meta.Exists = false
		return meta, nil
	}
	if err != nil {
		return nil, err
	}
	if relKind != "r" && relKind != "p" {
		meta.Exists = false
		return meta, nil
	}
	meta.Exists = true
	meta.IsPartitioned = relKind == "p"
	meta.EstimatedRows = int64(relTuples)

	// Column definitions
	colRows, err := q.QueryContext(ctx, `
		SELECT a.attname,
		       a.attnum::int,
		       t.typname,
		       pg_catalog.format_type(a.atttypid, a.atttypmod) || repeat('[]', GREATEST(a.attndims - 1, 0)),
		       NOT a.attnotnull,
		       pg_catalog.pg_get_expr(ad.adbin, ad.adrelid),
		       a.attidentity::text,
		       a.attgenerated::text,
		       COALESCE(pg_catalog.col_description(a.attrelid, a.attnum), '')
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
		LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, relOID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = colRows.Close() }()
	attNumToName := map[int]string{}
	colNullable := map[string]bool{}
	for colRows.Next() {
		var c RollbackColumnMeta
		var defExpr sql.NullString
		var attIdentity, attGenerated string
		if err := colRows.Scan(
			&c.Name, &c.Ordinal, &c.DataType, &c.ColumnType,
			&c.Nullable, &defExpr, &attIdentity, &attGenerated, &c.Comment,
		); err != nil {
			return nil, err
		}
		if defExpr.Valid {
			v := defExpr.String
			if attGenerated != "" {
				c.IsGenerated = true
				c.GenerationExpr = v
			} else {
				c.DefaultExpr = &v
				if strings.HasPrefix(strings.ToLower(v), "nextval(") {
					c.IsAutoIncrement = true
				}
			}
		}
		switch attIdentity {
		case "a":
			c.IsIdentity = true
			c.IsAutoIncrement = true
			c.IdentityGeneration = "ALWAYS"
		case "d":
			c.IsIdentity = true
			c.IsAutoIncrement = true
			c.IdentityGeneration = "BY DEFAULT"
		}
		attNumToName[c.Ordinal] = c.Name
		colNullable[strings.ToLower(c.Name)] = c.Nullable
		meta.Columns = append(meta.Columns, c)
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	// Indexes and primary key
	idxRows, err := q.QueryContext(ctx, `
		SELECT ic.relname,
		       i.indisunique,
		       i.indisprimary,
		       COALESCE(pa.attname, ''),
		       k.ord::int,
		       k.attnum = 0,
		       pg_catalog.pg_get_indexdef(i.indexrelid)
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		LEFT JOIN pg_catalog.pg_attribute pa ON pa.attrelid = i.indrelid AND pa.attnum = k.attnum
		WHERE i.indrelid = $1 AND k.ord <= i.indnkeyatts
		ORDER BY ic.relname, k.ord`, relOID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = idxRows.Close() }()
	type pgIdxAcc struct {
		meta    RollbackIndexMeta
		hasExpr bool
	}
	idxMap := map[string]*pgIdxAcc{}
	var idxOrder []string
	pkPos := map[string]int{}
	for idxRows.Next() {
		var idxName, colName, indexDef string
		var isUnique, isPrimary, isExpr bool
		var ord int
		if err := idxRows.Scan(&idxName, &isUnique, &isPrimary, &colName, &ord, &isExpr, &indexDef); err != nil {
			return nil, err
		}
		acc, ok := idxMap[idxName]
		if !ok {
			acc = &pgIdxAcc{
				meta: RollbackIndexMeta{
					Name:      idxName,
					Unique:    isUnique,
					Primary:   isPrimary,
					CreateDDL: indexDef,
				},
			}
			idxMap[idxName] = acc
			idxOrder = append(idxOrder, idxName)
		}
		if isExpr || colName == "" {
			acc.hasExpr = true
		} else {
			acc.meta.Columns = append(acc.meta.Columns, colName)
			if isPrimary {
				pkPos[colName] = ord
			}
		}
	}
	if err := idxRows.Err(); err != nil {
		return nil, err
	}
	for i := range meta.Columns {
		if pos, ok := pkPos[meta.Columns[i].Name]; ok {
			meta.Columns[i].IsPrimaryKey = true
			meta.Columns[i].PKOrdinal = pos
		}
	}
	for _, name := range idxOrder {
		acc := idxMap[name]
		meta.Indexes = append(meta.Indexes, acc.meta)
		if acc.meta.Primary && !acc.hasExpr && len(acc.meta.Columns) > 0 {
			meta.HasExplicitPrimaryKey = true
			meta.IdentityKeyColumns = append([]string(nil), acc.meta.Columns...)
		}
	}
	if len(meta.IdentityKeyColumns) == 0 {
		for _, name := range idxOrder {
			acc := idxMap[name]
			if !acc.meta.Unique || acc.hasExpr || len(acc.meta.Columns) == 0 {
				continue
			}
			allNotNull := true
			for _, col := range acc.meta.Columns {
				if colNullable[strings.ToLower(col)] {
					allNotNull = false
					break
				}
			}
			if allNotNull {
				meta.IdentityKeyColumns = append([]string(nil), acc.meta.Columns...)
				break
			}
		}
	}

	// Table-level constraints
	conRows, err := q.QueryContext(ctx, `
		SELECT con.conname,
		       con.contype::text,
		       pg_catalog.pg_get_constraintdef(con.oid, true)
		FROM pg_catalog.pg_constraint con
		WHERE con.conrelid = $1 AND con.contype IN ('p', 'u', 'c', 'f')
		ORDER BY con.conname`, relOID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conRows.Close() }()
	for conRows.Next() {
		var name, ctype, def string
		if err := conRows.Scan(&name, &ctype, &def); err != nil {
			return nil, err
		}
		mappedType := ctype
		switch ctype {
		case "p":
			mappedType = "PRIMARY KEY"
		case "u":
			mappedType = "UNIQUE"
		case "c":
			mappedType = "CHECK"
		case "f":
			mappedType = "FOREIGN KEY"
		}
		meta.Constraints = append(meta.Constraints, RollbackConstraintMeta{
			Name:       name,
			Type:       mappedType,
			Definition: def,
		})
	}
	if err := conRows.Err(); err != nil {
		return nil, err
	}

	// Non-internal trigger count
	_ = q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_catalog.pg_trigger
		WHERE tgrelid = $1 AND NOT tgisinternal`, relOID).Scan(&meta.TriggerCount)

	// Child-table foreign key cascade rules referencing this table
	fkRows, err := q.QueryContext(ctx, `
		SELECT cn.nspname, cc.relname, con.conname, con.confupdtype::text, con.confdeltype::text
		FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class cc ON cc.oid = con.conrelid
		JOIN pg_catalog.pg_namespace cn ON cn.oid = cc.relnamespace
		WHERE con.confrelid = $1 AND con.contype = 'f'`, relOID)
	if err == nil {
		defer func() { _ = fkRows.Close() }()
		for fkRows.Next() {
			var fk RollbackForeignKeyMeta
			var updCode, delCode string
			if err := fkRows.Scan(&fk.ChildSchema, &fk.ChildTable, &fk.ConstraintName, &updCode, &delCode); err == nil {
				fk.UpdateRule = mapPGFKAction(updCode)
				fk.DeleteRule = mapPGFKAction(delCode)
				meta.IncomingForeignKeys = append(meta.IncomingForeignKeys, fk)
			}
		}
	}

	meta.CreateTableDDL = buildPostgresCreateTableDDL(meta)
	return meta, nil
}
