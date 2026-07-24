package repository

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// Create inserts a new record and returns it with the generated ID (for auto-increment).
func (r *BaseRepository[T, ID]) Create(ctx context.Context, data *T) (*T, error) {
	if data == nil {
		return nil, fmt.Errorf("create: data is nil")
	}

	cols := r.insertCols
	values := r.getStructValues(data, cols)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		r.TableName,
		strings.Join(cols, ", "),
		r.Dialect.placeholders(1, len(cols)),
	)
	if r.Dialect.supportsReturning() {
		row := r.DB.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		return r.scanRow(row)
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

	cols := r.insertCols
	values := r.getStructValues(data, cols)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		r.TableName,
		strings.Join(cols, ", "),
		r.Dialect.placeholders(1, len(cols)),
	)
	if r.Dialect.supportsReturning() {
		row := tx.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		return r.scanRow(row)
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

// InsertMany inserts multiple records in one or more batches and returns
// the total number of affected rows.
func (r *BaseRepository[T, ID]) InsertMany(ctx context.Context, data []T) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	cols := r.insertCols
	colCount := len(cols)
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

		valueParts := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*colCount)

		for i := range chunk {
			offset := i * colCount
			valueParts = append(valueParts, fmt.Sprintf("(%s)", r.Dialect.placeholders(offset+1, colCount)))

			v := reflect.ValueOf(&chunk[i]).Elem()
			for _, col := range cols {
				if idx, ok := r.dbTagMap[col]; ok {
					args = append(args, v.Field(idx).Interface())
				}
			}
		}

		query := fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES %s",
			r.TableName,
			strings.Join(cols, ", "),
			strings.Join(valueParts, ", "),
		)

		result, err := r.DB.ExecContext(ctx, query, args...)
		if err != nil {
			return totalAffected, fmt.Errorf("insert many error: %w", err)
		}
		if n, err := result.RowsAffected(); err == nil {
			totalAffected += n
		}
	}

	return totalAffected, nil
}
