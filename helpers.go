package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/valyala/fastjson"
)

type DBNotification struct {
	ID        string
	Pubkey    string
	CreatedAt int64
	Kind      int
	Etags     []string
	Ptags     []string
	Dtag      string
	Gtags     []string
	Raw       []byte
}

type ParsedFilter struct {
	Authors []string
	Ptags   []string
	Etags   []string
	Kinds   []int
	IDs     []string
	Since   *int64
	Until   *int64
	Limit   *int
	Dtags   []string
	Gtags   []string
}

type Message struct {
	jmsg []json.RawMessage
	pool *sync.Pool
}

func (msg *Message) Release() {
	msg.pool.Put(msg.jmsg)
}

type Query struct {
	sql    string
	params []any
	pool   *sync.Pool
}

func (q *Query) Release() {
	q.pool.Put(q.params)
}

func SQL(filters []ParsedFilter, string_buf_pool *sync.Pool, any_buf_pool *sync.Pool, max_limit int) (*Query, error) {
	queries := string_buf_pool.Get().([]string)
	params := any_buf_pool.Get().([]any)
	defer string_buf_pool.Put(queries)
	queries = queries[:0]
	params = params[:0]
	var limit int
	for _, q := range filters {
		if q.Limit != nil && limit < *q.Limit {
			limit = *q.Limit
		}
		if s, p, e := q.sql(params, string_buf_pool); e != nil {
			return nil, e
		} else {
			params = p
			queries = append(queries, "("+s+")")
		}
	}
	if limit == 0 || limit > max_limit {
		limit = max_limit
	}
	params = append(params, fmt.Sprintf("%d", limit))
	sql := "SELECT raw FROM db1 WHERE " + strings.Join(queries, " OR ") + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(params))

	return &Query{sql, params, any_buf_pool}, nil
}

func (req *ReqSubmission) Cull(pf_buf []ParsedFilter) error {
	pf_buf = req.filters
	req.filters = req.filters[:0]
	for _, f := range pf_buf {
		// remove any filters which have IDs or Until field set
		if f.IDs != nil || f.Until != nil {
			continue
		}
		req.filters = append(req.filters, f)
	}
	if len(req.filters) == 0 {
		return fmt.Errorf("no filters remain")
	}
	return nil
}

func (q ParsedFilter) sql(params []any, string_buf_pool *sync.Pool) (string, []any, error) {
	buffer1 := string_buf_pool.Get().([]string)
	buffer2 := string_buf_pool.Get().([]string)
	defer string_buf_pool.Put(buffer1)
	defer string_buf_pool.Put(buffer2)
	counter := len(params)
	buffer1 = buffer1[:0]
	buffer2 = buffer2[:0]
	if len(q.Authors) > 0 {
		for _, key := range q.Authors {
			if len(key)%2 != 0 {
				key = key[:len(key)-1]
			}
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) > 32 {
				continue
			}
			counter++
			params = append(params, fmt.Sprintf("%x%%", parsed))
			buffer2 = append(buffer2, fmt.Sprintf("pubkey LIKE $%d", counter))
		}
		if len(buffer2) == 0 {
			// authors being [] mean you won't get anything
			return "", nil, fmt.Errorf("invalid authors field")
		}
		buffer1 = append(buffer1, "("+strings.Join(buffer2, " OR ")+")")
	}

	buffer2 = buffer2[:0]
	if len(q.IDs) > 0 {
		for _, key := range q.IDs {
			// prevent sql attack here!
			if len(key)%2 != 0 {
				key = key[:len(key)-1]
			}
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) > 32 {
				continue
			}
			counter++
			params = append(params, fmt.Sprintf("%x%%", parsed))
			buffer2 = append(buffer2, fmt.Sprintf("id LIKE $%d", counter))
		}
		if len(buffer2) == 0 {
			// ids being [] mean you won't get anything
			return "", nil, fmt.Errorf("invalid ids field")
		}
		buffer1 = append(buffer1, "("+strings.Join(buffer2, " OR ")+")")
	}

	buffer2 = buffer2[:0]
	if len(q.Ptags) > 0 {
		for _, key := range q.Ptags {
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) != 32 {
				continue
			}
			counter++
			buffer2 = append(buffer2, fmt.Sprintf("$%d", counter))
			params = append(params, fmt.Sprintf("%x", parsed))
		}
		if len(buffer2) == 0 {
			// ptags being [] mean you won't get anything
			return "", nil, fmt.Errorf("invalid #p tags")
		}
		buffer1 = append(buffer1, fmt.Sprintf("ptags && ARRAY[%s]", strings.Join(buffer2, ",")))
	}

	buffer2 = buffer2[:0]
	if len(q.Etags) > 0 {
		for _, key := range q.Etags {
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) != 32 {
				continue
			}
			counter++
			buffer2 = append(buffer2, fmt.Sprintf("$%d", counter))
			params = append(params, fmt.Sprintf("%x", parsed))
		}
		if len(buffer2) == 0 {
			return "", nil, fmt.Errorf("invalid #e tags")
		}
		buffer1 = append(buffer1, fmt.Sprintf("etags && ARRAY[%s]", strings.Join(buffer2, ",")))
	}

	buffer2 = buffer2[:0]
	if len(q.Gtags) > 0 {
		for _, key := range q.Gtags {
			counter++
			params = append(params, key)
			buffer2 = append(buffer2, fmt.Sprintf("$%d", counter))
		}
		buffer1 = append(buffer1, fmt.Sprintf("gtags && ARRAY[%s]", strings.Join(buffer2, ",")))
	}

	buffer2 = buffer2[:0]
	if len(q.Dtags) > 0 {
		for _, key := range q.Dtags {
			counter++
			params = append(params, key)
			buffer2 = append(buffer2, fmt.Sprintf("$%d", counter))
		}
		buffer1 = append(buffer1, fmt.Sprintf("ARRAY[dtag] && ARRAY[%s]", strings.Join(buffer2, ",")))
	}

	buffer2 = buffer2[:0]
	if len(q.Kinds) > 0 {
		// no sql injection issues since these are ints
		for _, kind := range q.Kinds {
			counter++
			params = append(params, kind)
			buffer2 = append(buffer2, fmt.Sprintf("$%d", counter))
		}
		buffer1 = append(buffer1, `kind IN (`+strings.Join(buffer2, ",")+`)`)
	}

	if q.Since != nil {
		counter++
		params = append(params, *q.Since)
		buffer1 = append(buffer1, fmt.Sprintf("created_at > $%d", counter))
	}
	if q.Until != nil {
		counter++
		params = append(params, *q.Until)
		buffer1 = append(buffer1, fmt.Sprintf("created_at < $%d", counter))
	}
	if len(buffer1) == 0 {
		// fallback
		buffer1 = append(buffer1, "true")
	}
	return strings.Join(buffer1, " AND "), params, nil
}

func (q *ParsedFilter) UnmarshalJSON(payload []byte) error {
	var fastjsonParser fastjson.Parser
	parsed, err := fastjsonParser.ParseBytes(payload)
	if err != nil {
		return fmt.Errorf("failed to parse filter: %w", err)
	}

	obj, err := parsed.Object()
	if err != nil {
		return fmt.Errorf("filter is not an object")
	}

	var visiterr error
	obj.Visit(func(k []byte, v *fastjson.Value) {
		if visiterr != nil {
			return
		}
		key := string(k)
		switch key {
		case "ids":
			q.IDs, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid 'ids' field: %w", err)
			}
		case "kinds":
			q.Kinds, err = fastjsonArrayToIntList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid 'kinds' field: %w", err)
			}
		case "authors":
			q.Authors, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid 'authors' field: %w", err)
			}
		case "since":
			val, err := v.Int64()
			if err != nil || val > math.MaxInt32 {
				visiterr = fmt.Errorf("invalid 'since' field: %w", err)
			}
			q.Since = &val
		case "until":
			val, err := v.Int64()
			if err != nil || val > math.MaxInt32 {
				visiterr = fmt.Errorf("invalid 'until' field: %w", err)
			}
			q.Until = &val
		case "limit":
			val, err := v.Int()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'limit' field: %w", err)
			}
			q.Limit = &val
		case "#p":
			q.Ptags, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid '#p' field: %w", err)
			}
		case "#e":
			q.Etags, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid '#e' field: %w", err)
			}
		case "#d":
			q.Dtags, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid '#d' field: %w", err)
			}
		default:
			if len(key) != 2 {
				visiterr = fmt.Errorf("cannot query for key %s", key)
			} else {
				if tmp, err := fastjsonArrayToStringList(v); err == nil {
					for _, s := range tmp {
						q.Gtags = append(q.Gtags, key+":"+s)
					}
				} else {
					visiterr = fmt.Errorf("invalid %s field: %w", key, err)
				}
			}
		}
	})
	if visiterr != nil {
		return visiterr
	}
	return nil
}

func (p *DBNotification) UnmarshalJSON(payload []byte) error {
	var fastjsonParser fastjson.Parser
	parsed, err := fastjsonParser.ParseBytes(payload)
	if err != nil {
		return fmt.Errorf("failed to parse notification: %w", err)
	}

	obj, err := parsed.Object()
	if err != nil {
		return fmt.Errorf("not an object!")
	}

	var visiterr error
	obj.Visit(func(k []byte, v *fastjson.Value) {
		key := string(k)
		switch key {
		case "id":
			sb, err := v.StringBytes()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'id' field: %w", err)
				return
			}
			p.ID = string(sb)
		case "pubkey":
			sb, err := v.StringBytes()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'pubkey' field: %w", err)
				return
			}
			p.Pubkey = string(sb)
		case "created_at":
			val, err := v.Int64()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'created_at' field: %w", err)
				return
			}
			p.CreatedAt = val
		case "kind":
			val, err := v.Int()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'kind' field: %w", err)
				return
			}
			p.Kind = val
		case "ptags":
			p.Ptags, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid 'ptags' field: %w", err)
			}
		case "etags":
			p.Etags, err = fastjsonArrayToStringList(v)
			if err != nil {
				visiterr = fmt.Errorf("invalid 'etags' field: %w", err)
			}
		case "raw":
			p.Raw = v.MarshalTo(p.Raw[:0])
			if err != nil {
				visiterr = fmt.Errorf("invalid 'raw' field: %w", err)
			}
		}
	})
	if visiterr != nil {
		return visiterr
	}
	return nil
}

// Match returns true if the q filter accepts the p DBNotification.
func (q ParsedFilter) Accept(p *DBNotification) (accept bool) {
	// authors
	if p == nil {
		return
	}
	if len(q.Authors) == 0 {
		goto ptags
	}
	for _, b := range q.Authors {
		if strings.HasPrefix(p.Pubkey, b) {
			goto ptags
		}

	}
	return
ptags:
	if len(q.Ptags) == 0 {
		goto etags
	}
	for _, a := range p.Ptags {
		for _, b := range q.Ptags {
			if b == a {
				goto etags
			}
		}
	}
	return
etags:
	if len(q.Etags) == 0 {
		goto kinds
	}
	for _, a := range p.Etags {
		for _, b := range q.Etags {
			if b == a {
				goto kinds
			}
		}
	}
	return
kinds:
	if len(q.Kinds) == 0 {
		goto dtags
	}
	for _, b := range q.Kinds {
		if b == p.Kind {
			goto dtags
		}
	}
	return
dtags:
	if len(q.Dtags) == 0 {
		goto gtags
	}
	for _, b := range q.Dtags {
		if b == p.Dtag {
			goto gtags
		}
	}
	return
gtags:
	if len(q.Gtags) == 0 {
		goto ids
	}
	for _, a := range p.Gtags {
		for _, b := range q.Gtags {
			if b == a {
				goto ids
			}
		}
	}
	return
ids:
	if len(q.IDs) == 0 {
		goto until
	}
	for _, b := range q.IDs {
		if strings.HasPrefix(p.ID, b) {
			goto until
		}
	}
	return
until:
	if q.Until == nil {
		goto since
	}
	if *q.Until > p.CreatedAt {
		goto since
	}
	return
since:
	if q.Since == nil {
		return true
	}
	if *q.Since < p.CreatedAt {
		return true
	}
	return
}

// fastjson helpers
func fastjsonArrayToStringList(v *fastjson.Value) ([]string, error) {
	arr, err := v.Array()
	if err != nil {
		return nil, err
	}

	sl := make([]string, len(arr))
	for i, v := range arr {
		sb, err := v.StringBytes()
		if err != nil {
			return nil, err
		}
		sl[i] = string(sb)
	}

	return sl, nil
}

func fastjsonArrayToIntList(v *fastjson.Value) ([]int, error) {
	arr, err := v.Array()
	if err != nil {
		return nil, err
	}

	il := make([]int, len(arr))
	for i, v := range arr {
		il[i], err = v.Int()
		if err != nil {
			return nil, err
		}
	}

	return il, nil
}

// to prevent SQL injections, use $tag$ string $tag$ construction, with random tag
func gen_sql_dollar_quote(b [32]byte) string {
	for i, x := range b {
		if x > 128 {
			x = x - 128
		}
		if x < 65 {
			x = 65 + x/3
		}
		if x > 90 {
			x = x + 7
		}
		if x > 122 {
			x = 122
		}
		b[i] = x
	}
	return string(b[:])
}
