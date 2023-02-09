package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/valyala/fastjson"
)

type DBNotification struct {
	ID        string
	Pubkey    string
	CreatedAt int64
	Kind      int
	Etags     []string
	Ptags     []string
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
}

const max_limit = 25

func (req ReqSubmission) SQL() (string, error) {
	queries := make([]string, len(req.filters))
	var limit int
	for i, q := range req.filters {
		if q.Limit != nil && limit < *q.Limit {
			limit = *q.Limit
		}
		if s, e := q.sql(); e != nil {
			return "", e
		} else {
			queries[i] = "(" + s + ")"
		}
	}
	if limit == 0 || limit > max_limit {
		limit = max_limit
	}
	query := "SELECT raw FROM db1 WHERE " + strings.Join(queries, " OR ") + fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", limit)
	return query, nil
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

func (q ParsedFilter) sql() (query string, err error) {
	conditions := make([]string, 0)
	if len(q.Authors) > 0 {
		likekeys := make([]string, 0, len(q.Authors))
		for _, key := range q.Authors {
			if len(key)%2 != 0 {
				key = key[:len(key)-1]
			}
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) > 32 {
				continue
			}
			likekeys = append(likekeys, fmt.Sprintf("pubkey LIKE '%x%%'", parsed))
		}
		if len(likekeys) == 0 {
			// authors being [] mean you won't get anything
			err = fmt.Errorf("invalid authors field")
			return
		}
		conditions = append(conditions, "("+strings.Join(likekeys, " OR ")+")")
	}
	if len(q.IDs) > 0 {
		likeids := make([]string, 0, len(q.IDs))
		for _, key := range q.IDs {
			// prevent sql attack here!
			if len(key)%2 != 0 {
				key = key[:len(key)-1]
			}
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) > 32 {
				continue
			}
			likeids = append(likeids, fmt.Sprintf("id LIKE '%x%%'", parsed))
		}
		if len(likeids) == 0 {
			// ids being [] mean you won't get anything
			err = fmt.Errorf("invalid ids field")
			return
		}
		conditions = append(conditions, "("+strings.Join(likeids, " OR ")+")")
	}
	if len(q.Ptags) > 0 {
		array_tags := make([]string, 0, len(q.Ptags))
		for _, key := range q.Ptags {
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) != 32 {
				continue
			}
			array_tags = append(array_tags, fmt.Sprintf("'%x'", parsed))
		}
		if len(array_tags) == 0 {
			// ptags being [] mean you won't get anything
			err = fmt.Errorf("invalid #p tags")
			return
		}
		conditions = append(conditions, fmt.Sprintf("ptags && ARRAY[%s]", strings.Join(array_tags, ",")))
	}

	if len(q.Etags) > 0 {
		array_tags := make([]string, 0, len(q.Etags))
		for _, key := range q.Etags {
			// prevent sql attack here!
			parsed, e := hex.DecodeString(key)
			if e != nil || len(parsed) != 32 {
				continue
			}
			array_tags = append(array_tags, fmt.Sprintf("'%x'", parsed))
		}
		if len(array_tags) == 0 {
			err = fmt.Errorf("invalid #e tags")
			return
		}
		conditions = append(conditions, fmt.Sprintf("etags && ARRAY[%s]", strings.Join(array_tags, ",")))
	}
	if len(q.Kinds) > 0 {
		// no sql injection issues since these are ints
		inkinds := make([]string, len(q.Kinds))
		for i, kind := range q.Kinds {
			inkinds[i] = strconv.Itoa(kind)
		}
		conditions = append(conditions, `kind IN (`+strings.Join(inkinds, ",")+`)`)
	}
	if q.Since != nil {
		conditions = append(conditions, fmt.Sprintf("created_at > %d", *q.Since))
	}
	if q.Until != nil {
		conditions = append(conditions, fmt.Sprintf("created_at < %d", *q.Until))
	}
	if len(conditions) == 0 {
		// fallback
		conditions = append(conditions, "true")
	}
	return strings.Join(conditions, " AND "), nil
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
			if err != nil {
				visiterr = fmt.Errorf("invalid 'since' field: %w", err)
			}
			q.Since = &val
		case "until":
			val, err := v.Int64()
			if err != nil {
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
			visiterr = fmt.Errorf("cannot query for key %s", key)
		}
	})
	if visiterr != nil {
		return visiterr
	}
	return nil
}

//{"created_at":1675736335,"kind":1,"etags":[],"ptags":[],"raw":{

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
		goto ids
	}
	for _, b := range q.Kinds {
		if b == p.Kind {
			goto ids
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
