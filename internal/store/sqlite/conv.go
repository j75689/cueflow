package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// Every value crosses into SQLite through arg and back through dest, so the columns hold one form
// of each type (see migrations/0027_baseline.sql): times as fixed-width UTC text, ids as lowercase
// text, lists and maps as JSON text.

// timeLayout is fixed width, so comparing two stored times as text compares the times.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// args converts query arguments with arg and adds $now, the time every query sees as now.
func args(in []any) []any {
	out := make([]any, len(in), len(in)+1)
	for i, v := range in {
		out[i] = arg(v)
	}
	return append(out, sql.Named("now", fmtTime(time.Now())))
}

// arg is v as its column stores it; a nil pointer, slice or map is NULL.
func arg(v any) any {
	switch v := v.(type) {
	case time.Time:
		return fmtTime(v)
	case *time.Time:
		if v == nil {
			return nil
		}
		return fmtTime(*v)
	case uuid.UUID:
		return v.String()
	case *uuid.UUID:
		if v == nil {
			return nil
		}
		return v.String()
	case json.RawMessage:
		if v == nil {
			return nil
		}
		return string(v)
	case []string, []uuid.UUID, map[string]any, map[string]string:
		if reflect.ValueOf(v).IsNil() {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err) // these types always marshal
		}
		return string(raw)
	}
	return v
}

// jsonArg is JSON bytes as the TEXT a JSON column holds; nil is NULL.
func jsonArg(raw []byte) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

// dests wraps scan destinations with dest.
func dests(in []any) []any {
	out := make([]any, len(in))
	for i, d := range in {
		out[i] = dest(d)
	}
	return out
}

// dest reads a column into d in the form arg wrote it; other destinations are database/sql's.
func dest(d any) any {
	switch d := d.(type) {
	case *time.Time:
		return timeDest{d}
	case **time.Time:
		return nullTimeDest{d}
	case *[]string, *[]uuid.UUID, *map[string]any, *map[string]string:
		return jsonDest{d}
	case *json.RawMessage:
		return bytesDest{(*[]byte)(d)}
	case *[]byte:
		return bytesDest{d}
	}
	return d
}

type timeDest struct{ t *time.Time }

func (d timeDest) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("sqlite: time column holds %T", src)
	}
	t, err := parseTime(s)
	if err != nil {
		return err
	}
	*d.t = t
	return nil
}

type nullTimeDest struct{ t **time.Time }

func (d nullTimeDest) Scan(src any) error {
	if src == nil {
		*d.t = nil
		return nil
	}
	var t time.Time
	if err := (timeDest{&t}).Scan(src); err != nil {
		return err
	}
	*d.t = &t
	return nil
}

// jsonDest decodes a JSON column; NULL leaves the destination as it is.
type jsonDest struct{ v any }

func (d jsonDest) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		return nil
	case string:
		return json.Unmarshal([]byte(s), d.v)
	case []byte:
		return json.Unmarshal(s, d.v)
	}
	return fmt.Errorf("sqlite: JSON column holds %T", src)
}

// bytesDest reads a BLOB or a JSON TEXT column into bytes; NULL is nil.
type bytesDest struct{ b *[]byte }

func (d bytesDest) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*d.b = nil
	case string:
		*d.b = []byte(s)
	case []byte:
		*d.b = append([]byte(nil), s...)
	default:
		return fmt.Errorf("sqlite: bytes column holds %T", src)
	}
	return nil
}
