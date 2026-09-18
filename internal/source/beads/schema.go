package beads

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/DMokong/data-platform/internal/record"
)

// ColumnKind maps a driver column type, as reported by sql.ColumnType.DatabaseTypeName, to the
// record.Kind that carries it. go-sql-driver/mysql reports unsigned integers with an "UNSIGNED "
// prefix. TINYINT stays Int64 and JSON stays String, because ingestion never transforms. Any
// other type is an error naming columnName and databaseTypeName.
func ColumnKind(columnName, databaseTypeName string) (record.Kind, error) {
	switch databaseTypeName {
	case "CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT",
		"JSON", "ENUM", "SET", "DECIMAL":
		return record.String, nil
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT",
		"UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED MEDIUMINT", "UNSIGNED INT", "UNSIGNED BIGINT":
		return record.Int64, nil
	case "FLOAT", "DOUBLE":
		return record.Float64, nil
	case "DATETIME", "TIMESTAMP":
		return record.Timestamp, nil
	}
	return 0, fmt.Errorf("beads: column %q has database type %q, which maps to no record kind",
		columnName, databaseTypeName)
}

// timestampLayout parses a DATETIME that arrives as text rather than time.Time.
const timestampLayout = "2006-01-02 15:04:05.999999999"

// decodeValue converts one scanned driver value to the Go type record.Batch requires for kind:
// string, int64, float64, or a time.Time in time.UTC. NULL stays nil. With interpolateParams the
// driver uses the text protocol, so a value can arrive as its wire text ([]byte) as well as an
// already-parsed Go value; both decode to the same result. Only the representation changes,
// never the value, and anything that does not decode exactly is an error.
func decodeValue(kind record.Kind, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch kind {
	case record.String:
		switch x := v.(type) {
		case []byte:
			return string(x), nil
		case string:
			return x, nil
		}
	case record.Int64:
		switch x := v.(type) {
		case int64:
			return x, nil
		case int32:
			return int64(x), nil
		case int16:
			return int64(x), nil
		case int8:
			return int64(x), nil
		case int:
			return int64(x), nil
		case uint32:
			return int64(x), nil
		case uint16:
			return int64(x), nil
		case uint8:
			return int64(x), nil
		case uint64:
			return uint64ToInt64(x)
		case []byte:
			return parseInt64(string(x))
		case string:
			return parseInt64(x)
		}
	case record.Float64:
		switch x := v.(type) {
		case float64:
			return x, nil
		case float32:
			// The driver parses a FLOAT column into a float32. Widening it directly would
			// surface binary noise (0.1 becomes 0.10000000149011612); the shortest decimal that
			// round-trips the float32 is the text the server sent, so parse that instead.
			return strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		case []byte:
			return strconv.ParseFloat(string(x), 64)
		case string:
			return strconv.ParseFloat(x, 64)
		}
	case record.Timestamp:
		switch x := v.(type) {
		case time.Time:
			return wallClockUTC(x), nil
		case []byte:
			return time.ParseInLocation(timestampLayout, string(x), time.UTC)
		case string:
			return time.ParseInLocation(timestampLayout, x, time.UTC)
		}
	default:
		return nil, fmt.Errorf("unknown kind %d", int(kind))
	}
	return nil, fmt.Errorf("cannot decode %T value %v as %v", v, v, kind)
}

// wallClockUTC returns a time.Time in time.UTC carrying t's wall-clock fields unchanged. The
// source's DATETIME values are naive, so their digits are the value; they are never shifted by a
// zone offset.
func wallClockUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// uint64ToInt64 converts an unsigned value, failing rather than wrapping when it exceeds int64.
func uint64ToInt64(u uint64) (int64, error) {
	if u > math.MaxInt64 {
		return 0, fmt.Errorf("unsigned value %d overflows int64", u)
	}
	return int64(u), nil
}

// parseInt64 decodes an integer's wire text. A value above int64's range is an error.
func parseInt64(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return n, nil
	}
	if u, uerr := strconv.ParseUint(s, 10, 64); uerr == nil {
		return uint64ToInt64(u)
	}
	return 0, err
}
