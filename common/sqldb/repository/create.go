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
	cols := r.insertCols
	values := r.getStructValues(data, cols)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		r.TableName,
		strings.Join(cols, ", "),
		placeholders(1, len(cols)),
		r.columnsString(),
	)

	row := r.DB.QueryRowContext(ctx, query, values...)
	return r.scanRow(row)
}

// CreateTx inserts a new record within a transaction.
func (r *BaseRepository[T, ID]) CreateTx(ctx context.Context, tx *sql.Tx, data *T) (*T, error) {
	cols := r.insertCols
	values := r.getStructValues(data, cols)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		r.TableName,
		strings.Join(cols, ", "),
		placeholders(1, len(cols)),
		r.columnsString(),
	)

	row := tx.QueryRowContext(ctx, query, values...)
	return r.scanRow(row)
}

// InsertMany inserts multiple records in a single batch and returns the number of affected rows.
func (r *BaseRepository[T, ID]) InsertMany(ctx context.Context, data []T) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	cols := r.insertCols
	colCount := len(cols)

	// Build batch VALUES clause
	valueParts := make([]string, 0, len(data))
	args := make([]any, 0, len(data)*colCount)

	for i, item := range data {
		offset := i * colCount
		valueParts = append(valueParts, fmt.Sprintf("(%s)", placeholders(offset+1, colCount)))

		v := reflect.ValueOf(&item).Elem()
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
		return 0, fmt.Errorf("insert many error: %w", err)
	}

	return result.RowsAffected()
}
