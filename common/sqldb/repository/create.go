package repository

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// insertQuery renders the INSERT statement. Column names come from the cached,
// already-validated `db` tags, so nothing here can be influenced per call.
func (r *BaseRepository[T, ID]) insertQuery() string {
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		r.quotedTable,
		strings.Join(r.quotedInsert, ", "),
		r.Dialect.placeholders(1, len(r.insertCols)),
	)
}

// Create inserts a new record and returns it with the generated ID (for auto-increment).
func (r *BaseRepository[T, ID]) Create(ctx context.Context, data *T) (*T, error) {
	if data == nil {
		return nil, fmt.Errorf("create: data is nil")
	}
	if len(r.insertCols) == 0 {
		return nil, fmt.Errorf("create: no insertable columns")
	}

	values := r.getValues(data, r.insertIdx)
	query := r.insertQuery()

	if r.Dialect.supportsReturning() {
		row := r.DB.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		// A write that returned no row is a failure, not an empty result.
		return r.scanRowRequired(row)
	}

	result, err := r.DB.ExecContext(ctx, query, values...)
	if err != nil {
		return nil, err
	}
	if id, err := result.LastInsertId(); err == nil {
		r.setGeneratedID(data, id)
	}
	return data, nil
}

// CreateTx inserts a new record within a transaction.
func (r *BaseRepository[T, ID]) CreateTx(ctx context.Context, tx *sql.Tx, data *T) (*T, error) {
	if data == nil {
		return nil, fmt.Errorf("create: data is nil")
	}
	if tx == nil {
		return nil, fmt.Errorf("create: tx is nil")
	}
	if len(r.insertCols) == 0 {
		return nil, fmt.Errorf("create: no insertable columns")
	}

	values := r.getValues(data, r.insertIdx)
	query := r.insertQuery()

	if r.Dialect.supportsReturning() {
		row := tx.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		return r.scanRowRequired(row)
	}

	result, err := tx.ExecContext(ctx, query, values...)
	if err != nil {
		return nil, err
	}
	if id, err := result.LastInsertId(); err == nil {
		r.setGeneratedID(data, id)
	}
	return data, nil
}

// insertBatchLimit is the safety cap on rows per InsertMany batch. Databases
// (PostgreSQL ~65535 parameters, MySQL max_allowed_packet, SQLite 999) each
// have their own limits — we chunk under all of them.
const insertBatchLimit = 500

// insertManyQuery renders a multi-row INSERT for rowCount rows.
func (r *BaseRepository[T, ID]) insertManyQuery(rowCount int) string {
	colCount := len(r.insertCols)
	valueParts := make([]string, rowCount)
	for i := 0; i < rowCount; i++ {
		valueParts[i] = "(" + r.Dialect.placeholders(i*colCount+1, colCount) + ")"
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		r.quotedTable,
		strings.Join(r.quotedInsert, ", "),
		strings.Join(valueParts, ", "),
	)
}

// InsertMany inserts multiple records in one or more batches and returns
// the total number of affected rows.
func (r *BaseRepository[T, ID]) InsertMany(ctx context.Context, data []T) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	colCount := len(r.insertCols)
	if colCount == 0 {
		return 0, fmt.Errorf("insert many: no insertable columns")
	}

	var totalAffected int64
	for start := 0; start < len(data); start += insertBatchLimit {
		end := start + insertBatchLimit
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]

		args := make([]any, 0, len(chunk)*colCount)
		for i := range chunk {
			v := reflect.ValueOf(&chunk[i]).Elem()
			for _, idx := range r.insertIdx {
				args = append(args, v.Field(idx).Interface())
			}
		}

		result, err := r.DB.ExecContext(ctx, r.insertManyQuery(len(chunk)), args...)
		if err != nil {
			return totalAffected, fmt.Errorf("insert many error: %w", err)
		}
		if n, err := result.RowsAffected(); err == nil {
			totalAffected += n
		}
	}

	return totalAffected, nil
}
