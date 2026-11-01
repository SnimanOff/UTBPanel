package backup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Keep each record bounded while allowing arbitrarily many streamed rows.
type recordReader struct {
	r    io.Reader
	left int64
}

func (r *recordReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, fmt.Errorf("JSON record exceeds 1 MiB")
	}
	p = p[:min(int64(len(p)), r.left)]
	n, err := r.r.Read(p)
	r.left -= int64(n)
	return n, err
}

func ReadRowsDocument(input io.Reader, sections map[string]func(map[string]any) error) error {
	r := &recordReader{r: input, left: 1 << 20}
	dec := json.NewDecoder(r)
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("expected JSON object")
	}
	seen := map[string]bool{}
	version := 0
	for dec.More() {
		r.left = 1 << 20
		keyToken, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] {
			return fmt.Errorf("invalid or duplicate JSON section")
		}
		seen[key] = true
		if key == "version" {
			if err := dec.Decode(&version); err != nil {
				return err
			}
			continue
		}
		visit, ok := sections[key]
		if !ok {
			return fmt.Errorf("unknown JSON section %q", key)
		}
		token, err := dec.Token()
		if err != nil || token != json.Delim('[') {
			return fmt.Errorf("expected row array")
		}
		for dec.More() {
			r.left = 1 << 20
			var row map[string]any
			if err := dec.Decode(&row); err != nil {
				return err
			}
			if row == nil {
				return fmt.Errorf("expected row object")
			}
			if err := visit(row); err != nil {
				return err
			}
		}
		r.left = 1 << 20
		if _, err := dec.Token(); err != nil {
			return err
		}
	}
	r.left = 1 << 20
	if _, err := dec.Token(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	if version != 1 {
		return fmt.Errorf("unsupported rows version")
	}
	for key := range sections {
		if !seen[key] {
			return fmt.Errorf("missing JSON section %q", key)
		}
	}
	return nil
}

type TableRestorer struct {
	ctx   context.Context
	tx    *sql.Tx
	table string
	types map[string]string
}

// Table names are selected by the caller; values and columns in an archive
// never become SQL identifiers without checking the destination schema.
func NewTableRestorer(ctx context.Context, tx *sql.Tx, table string) (*TableRestorer, error) {
	switch table {
	case "tg_users", "bot_users", "bot_allow", "payments", "m_minute", "m_hour", "m_day", "m_uniq":
	default:
		return nil, fmt.Errorf("unsupported restore table")
	}
	r := &TableRestorer{ctx: ctx, tx: tx, table: table, types: map[string]string{}}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, notnull, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&id, &name, &kind, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		r.types[name] = strings.ToUpper(kind)
	}
	return r, rows.Err()
}
func (r *TableRestorer) Insert(row map[string]any, replace bool) error {
	keys := []string{}
	for key := range row {
		if _, ok := r.types[key]; !ok {
			return fmt.Errorf("unknown %s column %q", r.table, key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := []any{}
	columns := []string{}
	placeholders := []string{}
	for _, key := range keys {
		value := row[key]
		if value != nil {
			switch r.types[key] {
			case "BLOB":
				text, ok := value.(string)
				if !ok {
					return fmt.Errorf("expected base64 blob")
				}
				raw, err := base64.StdEncoding.DecodeString(text)
				if err != nil {
					return err
				}
				value = raw
			case "INTEGER":
				switch v := value.(type) {
				case json.Number:
					n, err := v.Int64()
					if err != nil {
						return err
					}
					value = n
				case int64:
				case bool:
				default:
					return fmt.Errorf("expected integer %s", key)
				}
			case "TEXT":
				if _, ok := value.(string); !ok {
					return fmt.Errorf("expected text %s", key)
				}
			}
		}
		values = append(values, value)
		columns = append(columns, `"`+key+`"`)
		placeholders = append(placeholders, "?")
	}
	prefix := "INSERT"
	if replace {
		prefix = "INSERT OR REPLACE"
	}
	_, err := r.tx.ExecContext(r.ctx, prefix+` INTO "`+r.table+`" (`+strings.Join(columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+")", values...)
	return err
}
