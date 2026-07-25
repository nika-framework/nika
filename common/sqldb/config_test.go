package sqldb

import (
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
		// wantMentions is checked in the error text so a misconfiguration tells
		// the developer what the supported values are.
		wantMentions []string
	}{
		{
			name: "postgres",
			cfg:  Config{Driver: DriverPostgres, DSN: "postgres://localhost/db"},
		},
		{
			name: "mysql",
			cfg:  Config{Driver: DriverMySQL, DSN: "user@tcp(localhost)/db"},
		},
		{
			name: "sqlite",
			cfg:  Config{Driver: DriverSQLite, DSN: "file:test.db"},
		},
		{
			name:         "missing driver",
			cfg:          Config{DSN: "postgres://localhost/db"},
			wantErr:      true,
			wantMentions: []string{"postgres", "mysql", "sqlite3"},
		},
		{
			name:         "unsupported driver",
			cfg:          Config{Driver: "oracle", DSN: "x"},
			wantErr:      true,
			wantMentions: []string{"oracle", "postgres", "mysql", "sqlite3"},
		},
		{
			name:         "case mismatch is not silently accepted",
			cfg:          Config{Driver: "Postgres", DSN: "x"},
			wantErr:      true,
			wantMentions: []string{"Postgres"},
		},
		{
			name:    "missing dsn",
			cfg:     Config{Driver: DriverPostgres},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.wantErr && err == nil {
				t.Fatalf("validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validate() = %v, want nil", err)
			}
			for _, want := range tc.wantMentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}
