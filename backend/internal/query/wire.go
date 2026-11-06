package query

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"obzly/internal/storage/segments"
)

// wireFragment is a Fragment on NATS. Segment stats stay behind but for
// whether a token filter exists: the executor only reads the files.
type wireFragment struct {
	ID       string                    `json:"id"`
	Stream   string                    `json:"stream"`
	Segments []*segments.SegmentRecord `json:"segments"`
	SQL      string                    `json:"sql"`
	Args     []wireArg                 `json:"args"`
	Tokens   []string                  `json:"tokens,omitempty"`
}

// wireArg keeps an argument's type, so a time binds as a timestamp rather
// than a string.
type wireArg struct {
	Kind  string `json:"k"`
	Value string `json:"v,omitempty"`
}

// wireReply is an executor's answer: where to fetch the result, or that the
// fragment had no readable files.
type wireReply struct {
	ResultID string `json:"resultId,omitempty"`
	Empty    bool   `json:"empty,omitempty"`
}

func encodeFragment(fragment Fragment) ([]byte, error) {
	if len(fragment.LocalFiles) > 0 {
		return nil, fmt.Errorf("fragment %s has local files and cannot run remotely", fragment.ID)
	}
	wire := wireFragment{ID: fragment.ID, Stream: fragment.Stream, SQL: fragment.SQL, Tokens: fragment.Tokens}
	for _, record := range fragment.Segments {
		stripped := *record
		stripped.Stats = nil
		if record.Stats != nil && record.Stats.TokenBloom {
			stripped.Stats = &segments.SegmentStats{TokenBloom: true}
		}
		wire.Segments = append(wire.Segments, &stripped)
	}
	for _, arg := range fragment.Args {
		encoded, err := encodeArg(arg)
		if err != nil {
			return nil, err
		}
		wire.Args = append(wire.Args, encoded)
	}
	return json.Marshal(wire)
}

func decodeFragment(data []byte) (Fragment, error) {
	var wire wireFragment
	if err := json.Unmarshal(data, &wire); err != nil {
		return Fragment{}, fmt.Errorf("invalid fragment: %w", err)
	}
	fragment := Fragment{ID: wire.ID, Stream: wire.Stream, Segments: wire.Segments, SQL: wire.SQL, Tokens: wire.Tokens}
	for _, arg := range wire.Args {
		decoded, err := decodeArg(arg)
		if err != nil {
			return Fragment{}, err
		}
		fragment.Args = append(fragment.Args, decoded)
	}
	return fragment, nil
}

func encodeArg(arg any) (wireArg, error) {
	switch value := arg.(type) {
	case nil:
		return wireArg{Kind: "n"}, nil
	case string:
		return wireArg{Kind: "s", Value: value}, nil
	case bool:
		return wireArg{Kind: "b", Value: strconv.FormatBool(value)}, nil
	case int:
		return wireArg{Kind: "i", Value: strconv.FormatInt(int64(value), 10)}, nil
	case int32:
		return wireArg{Kind: "i", Value: strconv.FormatInt(int64(value), 10)}, nil
	case int64:
		return wireArg{Kind: "i", Value: strconv.FormatInt(value, 10)}, nil
	case float64:
		return wireArg{Kind: "f", Value: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case time.Time:
		return wireArg{Kind: "t", Value: value.UTC().Format(time.RFC3339Nano)}, nil
	default:
		return wireArg{}, fmt.Errorf("query argument of type %T cannot be sent to an executor", arg)
	}
}

func decodeArg(arg wireArg) (any, error) {
	switch arg.Kind {
	case "n":
		return nil, nil
	case "s":
		return arg.Value, nil
	case "b":
		return strconv.ParseBool(arg.Value)
	case "i":
		return strconv.ParseInt(arg.Value, 10, 64)
	case "f":
		return strconv.ParseFloat(arg.Value, 64)
	case "t":
		return time.Parse(time.RFC3339Nano, arg.Value)
	default:
		return nil, fmt.Errorf("unknown query argument kind %q", arg.Kind)
	}
}
