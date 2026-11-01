// Package protocol defines the SQLite RPC contract independently of the
// driver and core, so the core does not need to import the client driver.
package protocol

import (
	"database/sql/driver"
	"fmt"
	"time"
)

const Version = 1
const BatchSize = 64

// Value preserves SQLite's five storage classes, including an empty BLOB.
type Value struct {
	Kind    string  `cbor:"1,keyasint"`
	Integer int64   `cbor:"2,keyasint,omitempty"`
	Real    float64 `cbor:"3,keyasint,omitempty"`
	Text    string  `cbor:"4,keyasint,omitempty"`
	Blob    []byte  `cbor:"5,keyasint,omitempty"`
}

func Encode(v any) (Value, error) {
	switch v := v.(type) {
	case nil:
		return Value{Kind: "null"}, nil
	case int64:
		return Value{Kind: "integer", Integer: v}, nil
	case float64:
		return Value{Kind: "real", Real: v}, nil
	case string:
		return Value{Kind: "text", Text: v}, nil
	case []byte:
		if v == nil {
			return Value{Kind: "null"}, nil
		}
		return Value{Kind: "blob", Blob: v}, nil
	case bool:
		n := int64(0)
		if v {
			n = 1
		}
		return Value{Kind: "integer", Integer: n}, nil
	case time.Time:
		return Value{Kind: "text", Text: v.Format(time.RFC3339Nano)}, nil
	default:
		return Value{}, fmt.Errorf("tgpanel db: unsupported value %T", v)
	}
}

func (v Value) Decode() (driver.Value, error) {
	switch v.Kind {
	case "null":
		return nil, nil
	case "integer":
		return v.Integer, nil
	case "real":
		return v.Real, nil
	case "text":
		return v.Text, nil
	case "blob":
		if v.Blob == nil {
			return []byte{}, nil
		}
		return v.Blob, nil
	default:
		return nil, fmt.Errorf("tgpanel db: invalid storage class %q", v.Kind)
	}
}

type Arg struct {
	Name    string `cbor:"1,keyasint,omitempty"`
	Ordinal int    `cbor:"2,keyasint"`
	Value   Value  `cbor:"3,keyasint"`
}

type Open struct {
	Version int `cbor:"1,keyasint"`
}
type Opened struct {
	ID      string `cbor:"1,keyasint"`
	Version int    `cbor:"2,keyasint"`
}

// Request addresses resources within one transport session, never a file path.
type Request struct {
	Conn      string `cbor:"1,keyasint"`
	SQL       string `cbor:"2,keyasint,omitempty"`
	Args      []Arg  `cbor:"3,keyasint,omitempty"`
	Stmt      string `cbor:"4,keyasint,omitempty"`
	Cursor    string `cbor:"5,keyasint,omitempty"`
	Isolation int    `cbor:"6,keyasint,omitempty"`
	ReadOnly  bool   `cbor:"7,keyasint,omitempty"`
}

type Prepared struct {
	ID       string `cbor:"1,keyasint"`
	NumInput int    `cbor:"2,keyasint"`
}
type Result struct {
	LastID   int64 `cbor:"1,keyasint"`
	Affected int64 `cbor:"2,keyasint"`
}
type Batch struct {
	Cursor  string    `cbor:"1,keyasint,omitempty"`
	Columns []string  `cbor:"2,keyasint,omitempty"`
	Types   []string  `cbor:"3,keyasint,omitempty"`
	Rows    [][]Value `cbor:"4,keyasint,omitempty"`
	Done    bool      `cbor:"5,keyasint"`
}
