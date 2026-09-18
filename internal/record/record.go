package record

import (
	"fmt"
	"time"
)

// Kind names the type carried by one Column.
type Kind int

const (
	// String is UTF-8 text (varchar, char, text, json).
	String Kind = iota + 1
	// Int64 is any integer.
	Int64
	// Float64 is any floating-point number.
	Float64
	// Bool is a boolean.
	Bool
	// Timestamp is a naive wall-clock timestamp: a time.Time in time.UTC whose wall fields are
	// the source's value.
	Timestamp
)

// String renders the Kind for error messages and logs.
func (k Kind) String() string {
	switch k {
	case String:
		return "string"
	case Int64:
		return "int64"
	case Float64:
		return "float64"
	case Bool:
		return "bool"
	case Timestamp:
		return "timestamp"
	default:
		return fmt.Sprintf("unknown kind(%d)", int(k))
	}
}

// Column names one field of a Batch and the Kind of value it carries.
type Column struct {
	Name string
	Kind Kind
}

// Batch is a schema-carrying, in-memory set of rows: each row has one value per Column, in
// Column order. A value is nil, or a string, int64, float64, bool or time.Time matching its
// column's Kind.
type Batch struct {
	Columns []Column
	Rows    [][]any
}

// Validate reports whether b is well-formed: no empty or duplicate column name, every column has
// a known Kind, every row has exactly len(Columns) values, every non-nil value's Go type matches
// its column's Kind, and every Timestamp value is located in time.UTC.
func (b Batch) Validate() error {
	seen := make(map[string]bool, len(b.Columns))
	for i, c := range b.Columns {
		if c.Name == "" {
			return fmt.Errorf("record: column %d has an empty name", i)
		}
		if seen[c.Name] {
			return fmt.Errorf("record: duplicate column name %q", c.Name)
		}
		seen[c.Name] = true
		switch c.Kind {
		case String, Int64, Float64, Bool, Timestamp:
		default:
			return fmt.Errorf("record: column %q has unknown kind %d", c.Name, int(c.Kind))
		}
	}
	for r, row := range b.Rows {
		if len(row) != len(b.Columns) {
			return fmt.Errorf("record: row %d has %d values, want %d", r, len(row), len(b.Columns))
		}
		for i, v := range row {
			if v == nil {
				continue
			}
			if err := validateValue(b.Columns[i], v); err != nil {
				return fmt.Errorf("record: row %d, column %q: %w", r, b.Columns[i].Name, err)
			}
		}
	}
	return nil
}

func validateValue(col Column, v any) error {
	switch col.Kind {
	case String:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("value %v (%T) is not a string, want Kind String", v, v)
		}
	case Int64:
		if _, ok := v.(int64); !ok {
			return fmt.Errorf("value %v (%T) is not an int64, want Kind Int64", v, v)
		}
	case Float64:
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("value %v (%T) is not a float64, want Kind Float64", v, v)
		}
	case Bool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("value %v (%T) is not a bool, want Kind Bool", v, v)
		}
	case Timestamp:
		t, ok := v.(time.Time)
		if !ok {
			return fmt.Errorf("value %v (%T) is not a time.Time, want Kind Timestamp", v, v)
		}
		if t.Location() != time.UTC {
			return fmt.Errorf("timestamp %v is located in %v, want time.UTC", v, t.Location())
		}
	default:
		return fmt.Errorf("column has unknown kind %d", int(col.Kind))
	}
	return nil
}
