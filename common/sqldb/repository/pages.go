package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

// maxPerPage caps how many rows one page may request. An unbounded perPage taken
// from a query string (`?perPage=100000000`) lets a client ask the process to
// materialise the whole table in memory.
const maxPerPage int64 = 500

// defaultPerPage is used when the caller passes a non-positive perPage.
const defaultPerPage int64 = 15

// queryer is the read subset shared by *sql.DB and *sql.Tx, so the pagination
// code can run either against the pool or inside a caller's transaction.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// direction returns the sort keyword. It is a constant lookup rather than a
// formatted string: interpolating a caller-supplied direction is how `?sort=id
// DESC; DROP TABLE users --` gets into a statement.
func (o OrderBy) direction() string {
	if o.Desc {
		return "DESC"
	}
	return "ASC"
}

// buildOrderBy validates and quotes every sort column. With no directive it
// falls back to the primary key so pagination is stable across pages.
func (r *BaseRepository[T, ID]) buildOrderBy(orderBy []OrderBy) (string, error) {
	if len(orderBy) == 0 {
		return "ORDER BY " + r.quotedID + " ASC", nil
	}

	parts := make([]string, 0, len(orderBy))
	for _, o := range orderBy {
		col, err := r.quote(o.Column)
		if err != nil {
			return "", fmt.Errorf("order by: %w", err)
		}
		parts = append(parts, col+" "+o.direction())
	}
	return "ORDER BY " + strings.Join(parts, ", "), nil
}

// normalizePageArgs clamps the page window into a sane range.
func normalizePageArgs(page, perPage int64) (int64, int64) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}

// pageQuery renders the page SELECT for a pre-built predicate.
func (r *BaseRepository[T, ID]) pageQuery(
	whereClause string,
	nextIdx int,
	orderBy []OrderBy,
) (string, error) {
	orderClause, err := r.buildOrderBy(orderBy)
	if err != nil {
		return "", err
	}

	return joinSQL(
		"SELECT "+r.columnsString(),
		"FROM "+r.quotedTable,
		whereClause,
		orderClause,
		"LIMIT "+r.Dialect.placeholder(nextIdx+1),
		"OFFSET "+r.Dialect.placeholder(nextIdx+2),
	), nil
}

// pagesQuery renders the count and page statements for a filter map. The page
// window itself only supplies bound arguments, so it plays no part here.
func (r *BaseRepository[T, ID]) pagesQuery(
	filter Filter,
	orderBy []OrderBy,
) (countQuery string, pageQuery string, whereArgs []any, err error) {
	whereClause, whereArgs, nextIdx, err := r.buildWhereNext(filter, 0)
	if err != nil {
		return "", "", nil, err
	}
	pageQuery, err = r.pageQuery(whereClause, nextIdx, orderBy)
	if err != nil {
		return "", "", nil, err
	}
	countQuery = joinSQL("SELECT COUNT(*) FROM "+r.quotedTable, whereClause)
	return countQuery, pageQuery, whereArgs, nil
}

// Pages returns paginated results with total count and metadata.
//
// CONSISTENCY: the count and the page are two separate round trips with no
// surrounding transaction, so under concurrent writes Total can disagree with
// len(Data) — a row inserted between the two statements is counted but not
// returned, or vice versa. That is acceptable for UI paging; when the numbers
// must agree, use PagesTx with a transaction (ideally REPEATABLE READ, see
// WithTransactionOpts).
//
// SCALE: LIMIT/OFFSET makes the database walk and discard `offset` rows, so deep
// pages get linearly slower. KeysetPage is the right tool past a few thousand
// rows.
func (r *BaseRepository[T, ID]) Pages(
	ctx context.Context,
	filter Filter,
	page int64,
	perPage int64,
	orderBy ...OrderBy,
) (*PaginationResult[T], error) {
	return r.pages(ctx, r.DB, filter, page, perPage, orderBy)
}

// PagesTx is Pages executed inside the caller's transaction, so the count and
// the page observe the same snapshot.
func (r *BaseRepository[T, ID]) PagesTx(
	ctx context.Context,
	tx *sql.Tx,
	filter Filter,
	page int64,
	perPage int64,
	orderBy ...OrderBy,
) (*PaginationResult[T], error) {
	if tx == nil {
		return nil, fmt.Errorf("pages: tx is nil")
	}
	return r.pages(ctx, tx, filter, page, perPage, orderBy)
}

func (r *BaseRepository[T, ID]) pages(
	ctx context.Context,
	q queryer,
	filter Filter,
	page int64,
	perPage int64,
	orderBy []OrderBy,
) (*PaginationResult[T], error) {
	page, perPage = normalizePageArgs(page, perPage)
	offset := (page - 1) * perPage

	countQuery, pageQuery, whereArgs, err := r.pagesQuery(filter, orderBy)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := q.QueryRowContext(ctx, countQuery, whereArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("pagination count error: %w", err)
	}

	if total == 0 {
		return &PaginationResult[T]{
			Data:       []T{},
			Total:      0,
			Page:       page,
			PerPage:    perPage,
			TotalPages: 0,
		}, nil
	}

	rows, err := q.QueryContext(ctx, pageQuery, concatArgs(whereArgs, []any{perPage, offset})...)
	if err != nil {
		return nil, fmt.Errorf("pagination query error: %w", err)
	}

	data, err := r.scanRows(rows)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []T{}
	}

	return &PaginationResult[T]{
		Data:       data,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: int64(math.Ceil(float64(total) / float64(perPage))),
	}, nil
}

// PagesByWhere paginates using the typed condition list.
func (r *BaseRepository[T, ID]) PagesByWhere(
	ctx context.Context,
	conds []Cond,
	page int64,
	perPage int64,
	orderBy ...OrderBy,
) (*PaginationResult[T], error) {
	page, perPage = normalizePageArgs(page, perPage)
	offset := (page - 1) * perPage

	whereClause, whereArgs, nextIdx, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return nil, err
	}
	pageQuery, err := r.pageQuery(whereClause, nextIdx, orderBy)
	if err != nil {
		return nil, err
	}
	countQuery := joinSQL("SELECT COUNT(*) FROM "+r.quotedTable, whereClause)

	var total int64
	if err := r.DB.QueryRowContext(ctx, countQuery, whereArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("pagination count error: %w", err)
	}
	if total == 0 {
		return &PaginationResult[T]{Data: []T{}, Page: page, PerPage: perPage}, nil
	}

	rows, err := r.DB.QueryContext(ctx, pageQuery, concatArgs(whereArgs, []any{perPage, offset})...)
	if err != nil {
		return nil, fmt.Errorf("pagination query error: %w", err)
	}
	data, err := r.scanRows(rows)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []T{}
	}

	return &PaginationResult[T]{
		Data:       data,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: int64(math.Ceil(float64(total) / float64(perPage))),
	}, nil
}

// KeysetResult is one page of cursor-based pagination.
type KeysetResult[T any, ID comparable] struct {
	Data []T `json:"data"`
	// NextCursor is the ID to pass as `after` for the following page. It is nil
	// when there is no further page.
	NextCursor *ID  `json:"nextCursor"`
	HasMore    bool `json:"hasMore"`
}

func (r *BaseRepository[T, ID]) keysetQuery(filter Filter, after *ID, limit int64, desc bool) (string, []any, error) {
	conds := filterToConds(filter)
	if after != nil {
		op := OpGT
		if desc {
			op = OpLT
		}
		conds = append(conds, Cond{Column: r.IDColumn, Op: op, Value: *after})
	}

	whereClause, args, nextIdx, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return "", nil, err
	}

	dir := "ASC"
	if desc {
		dir = "DESC"
	}

	query := joinSQL(
		"SELECT "+r.columnsString(),
		"FROM "+r.quotedTable,
		whereClause,
		"ORDER BY "+r.quotedID+" "+dir,
		"LIMIT "+r.Dialect.placeholder(nextIdx+1),
	)
	// Fetching one extra row is how HasMore is answered without a second query.
	return query, concatArgs(args, []any{limit + 1}), nil
}

// KeysetPage returns a page using cursor (keyset) pagination on the ID column.
//
// Unlike Pages it never issues an OFFSET, so page 10 000 costs the same as page
// 1: the database seeks straight to the cursor via the primary key index. The
// trade-off is that pages can only be walked in order — there is no "jump to
// page N" and no total count.
//
// Pass after=nil for the first page, then feed NextCursor back in.
func (r *BaseRepository[T, ID]) KeysetPage(
	ctx context.Context,
	filter Filter,
	after *ID,
	limit int64,
	desc bool,
) (*KeysetResult[T, ID], error) {
	if limit <= 0 {
		limit = defaultPerPage
	}
	if limit > maxPerPage {
		limit = maxPerPage
	}

	query, args, err := r.keysetQuery(filter, after, limit, desc)
	if err != nil {
		return nil, err
	}

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("keyset page error: %w", err)
	}
	data, err := r.scanRows(rows)
	if err != nil {
		return nil, err
	}

	out := &KeysetResult[T, ID]{Data: []T{}}
	if len(data) > int(limit) {
		out.HasMore = true
		data = data[:limit]
	}
	out.Data = data
	if out.Data == nil {
		out.Data = []T{}
	}

	if len(out.Data) > 0 {
		if id, ok := r.modelID(&out.Data[len(out.Data)-1]); ok && out.HasMore {
			out.NextCursor = &id
		}
	}

	return out, nil
}
