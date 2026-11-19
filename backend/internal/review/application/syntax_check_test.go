package application

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func TestCountAndValidateStatements_RejectsSyntaxErrorsWithLocation(t *testing.T) {
	svc := &Services{}
	pgProfile, err := fingerprint.ParseProfile("postgresql/postgresql")
	if err != nil {
		t.Fatalf("parse pg profile: %v", err)
	}
	mysqlProfile, err := fingerprint.ParseProfile("mysql/mysql")
	if err != nil {
		t.Fatalf("parse mysql profile: %v", err)
	}

	cases := []struct {
		name       string
		profile    fingerprint.Profile
		sql        string
		wantSubstr []string
	}{
		{
			name:       "pg create table missing column list",
			profile:    pgProfile,
			sql:        "create table x",
			wantSubstr: []string{"line 1, col 1", "statement #1", "postgresql/postgresql", "syntax error"},
		},
		{
			name:       "pg second statement syntax error preserves line and ordinal",
			profile:    pgProfile,
			sql:        "CREATE TABLE demo.ok (id INT PRIMARY KEY);\nCREATE TABLE demo.bad",
			wantSubstr: []string{"line 2, col 1", "statement #2", "postgresql/postgresql", "syntax error"},
		},
		{
			name:       "mysql create table missing column list",
			profile:    mysqlProfile,
			sql:        "create table x",
			wantSubstr: []string{"line 1, col 1", "statement #1", "mysql/mysql"},
		},
		{
			name:       "mysql create table empty column list",
			profile:    mysqlProfile,
			sql:        "CREATE TABLE x ()",
			wantSubstr: []string{"line 1, col 1", "statement #1", "mysql/mysql"},
		},
		{
			name:       "mysql update missing set clause",
			profile:    mysqlProfile,
			sql:        "UPDATE orders WHERE id = 1;",
			wantSubstr: []string{"line 1, col 1", "statement #1", "mysql/mysql"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.countAndValidateStatements([]byte(tc.sql), tc.profile)
			if err == nil {
				t.Fatalf("expected SQL_PARSE_FAILED for %q", tc.sql)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeSQLParseFailed {
				t.Fatalf("expected 2003 SQL_PARSE_FAILED, got %v", err)
			}
			for _, sub := range tc.wantSubstr {
				if !strings.Contains(de.Detail, sub) {
					t.Fatalf("detail %q missing %q", de.Detail, sub)
				}
			}
		})
	}
}

func TestCountAndValidateStatements_AcceptsValidDialectsAndCatchesLateBulkSyntaxError(t *testing.T) {
	svc := &Services{}
	pgProfile, _ := fingerprint.ParseProfile("postgresql/postgresql")
	mysqlProfile, _ := fingerprint.ParseProfile("mysql/mysql")

	validPG := "CREATE TABLE demo.x (id INT PRIMARY KEY, name TEXT NOT NULL);\n" +
		"CREATE INDEX idx_x_name ON demo.x (name);\n" +
		"UPDATE demo.x SET name = 'ok' WHERE id = 1;"
	cnt, err := svc.countAndValidateStatements([]byte(validPG), pgProfile)
	if err != nil {
		t.Fatalf("valid PG batch rejected: %v", err)
	}
	if cnt != 3 {
		t.Fatalf("got count=%d want 3", cnt)
	}

	validMySQL := "CREATE TABLE `orders` (`id` BIGINT PRIMARY KEY AUTO_INCREMENT, `status` TINYINT NOT NULL) ENGINE=InnoDB;\n" +
		"CREATE TABLE `orders_copy` LIKE `orders`;\n" +
		"ALTER TABLE `orders` ADD COLUMN `note` VARCHAR(255) DEFAULT NULL;\n" +
		"UPDATE `orders` SET `status` = 1 WHERE `id` = 42;"
	cnt, err = svc.countAndValidateStatements([]byte(validMySQL), mysqlProfile)
	if err != nil {
		t.Fatalf("valid MySQL batch rejected: %v", err)
	}
	if cnt != 4 {
		t.Fatalf("got count=%d want 4", cnt)
	}

	// Bulk batch (> syntaxCheckDirectLimit): 220 homogeneous valid INSERTs followed by 1 malformed DDL.
	var bulk strings.Builder
	for i := 1; i <= 220; i++ {
		fmt.Fprintf(&bulk, "INSERT INTO demo.logs (id, msg) VALUES (%d, 'row-%d');\n", i, i)
	}
	bulk.WriteString("CREATE TABLE broken_late_stmt;")
	_, err = svc.countAndValidateStatements([]byte(bulk.String()), pgProfile)
	if err == nil {
		t.Fatal("expected malformed statement #221 after bulk prefix to fail")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeSQLParseFailed {
		t.Fatalf("expected 2003 SQL_PARSE_FAILED, got %v", err)
	}
	if !strings.Contains(de.Detail, "statement #221") {
		t.Fatalf("expected statement #221 in detail, got %q", de.Detail)
	}
}
