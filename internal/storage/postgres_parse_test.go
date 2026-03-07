package storage

import (
	"reflect"
	"testing"
)

func TestParseSQLStatements(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected []string
	}{
		{
			name:     "single statement",
			sql:      "SELECT * FROM users;",
			expected: []string{"SELECT * FROM users"},
		},
		{
			name:     "multiple statements",
			sql:      "SELECT 1;\nSELECT 2;",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name: "semicolon in single quotes",
			sql:  "SELECT 'hello;world';",
			expected: []string{
				"SELECT 'hello;world'",
			},
		},
		{
			name: "semicolon in double quotes",
			sql:  `SELECT "hello;world";`,
			expected: []string{
				`SELECT "hello;world"`,
			},
		},
		{
			name: "apostrophe in line comment",
			sql: `
				-- This is a comment with an apostrophe: don't split here
				SELECT 1;
				SELECT 2;
			`,
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name: "apostrophe in block comment",
			sql: `
				/*
				  This comment shouldn't affect 'quotes' or "quotes"
				*/
				SELECT 1;
				SELECT 2;
			`,
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "statements without trailing semicolon",
			sql:      "SELECT 1;\nSELECT 2",
			expected: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:     "empty statements are ignored",
			sql:      ";;; \n  ;;",
			expected: nil,
		},
		{
			name: "statements with only comments are ignored",
			sql: `
				-- just a comment
				;
				/* another comment */
				;
			`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSQLStatements(tt.sql)
			if len(got) == 0 && len(tt.expected) == 0 {
				return // Both empty, success
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("parseSQLStatements() = %v, want %v", got, tt.expected)
			}
		})
	}
}
