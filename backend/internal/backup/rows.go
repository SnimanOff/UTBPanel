package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
)

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Rows writes one row at a time. SQLite blobs use JSON base64, retaining HLL
// sketches and the activity bitmap without precision or encoding changes.
func Rows(ctx context.Context, q Queryer, out io.Writer, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(out, "["); err != nil {
		return err
	}
	first := true
	for rows.Next() {
		values := make([]any, len(columns))
		scan := make([]any, len(columns))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return err
		}
		value := map[string]any{}
		for i, col := range columns {
			value[col] = values[i]
		}
		if !first {
			if _, err := io.WriteString(out, ","); err != nil {
				return err
			}
		}
		first = false
		if err := json.NewEncoder(out).Encode(value); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = io.WriteString(out, "]")
	return err
}
