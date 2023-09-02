package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/valyala/fastjson"
	"golang.org/x/time/rate"
)

type Settings struct {
	host                         string
	relay_url                    string
	nip11_info_document          []byte
	max_limit                    int
	subid_max_length             int
	websocket_rate_limit         rate.Limit
	websocket_burst              int
	delete_expired_events_period time.Duration
}

type Config struct {
	settings_chan chan *Settings
	nip11_chan    chan *NIP11_document
	done_chan     chan struct{}
}

func (c *Config) Settings() *Settings {
	return <-c.settings_chan
}
func (c *Config) NIP11() *NIP11_document {
	return <-c.nip11_chan
}

// Done() must be called after calling Settings() or NIP11()
func (c *Config) Done() {
	c.done_chan <- struct{}{}
}

// Config Hot Reloading
func InitConfig(filename string) (c *Config, e error) {
	s := new(Settings)
	c = &Config{
		settings_chan: make(chan *Settings),
		nip11_chan:    make(chan *NIP11_document),
		done_chan:     make(chan struct{}),
	}
	if e = s.parseSettings(filename); e != nil {
		return
	}
	doc := new(NIP11_document)
	if e = doc.Parse(s.nip11_info_document); e != nil {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		e = err
		return
	}

	// Add a path.
	if e = watcher.Add("."); e != nil {
		return
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					panic(ok)
				}
				if event.Has(fsnotify.Write) && event.Name == "./"+filename {
					if e = s.parseSettings(filename); e != nil {
						panic(e)
					}
					if e = doc.Parse(s.nip11_info_document); e != nil {
						panic(e)
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					panic(ok)
				}
			case c.settings_chan <- s:
				<-c.done_chan
			case c.nip11_chan <- doc:
				<-c.done_chan
			}
		}
	}()
	return c, e
}

func (s *Settings) parseSettings(filename string) (e error) {
	if f, e := os.Open(filename); e != nil {
		return e
	} else {
		defer f.Close()
		if b, e := io.ReadAll(f); e != nil {
			return e
		} else {
			if e := s.UnmarshalJSON(b); e != nil {
				return e
			}
		}
	}
	// require sane defaults
	if s.host == "" {
		s.host = "localhost:8080"
	}
	if s.max_limit == 0 {
		s.max_limit = 25
	}
	if s.subid_max_length == 0 {
		s.subid_max_length = 32
	}
	if s.websocket_rate_limit == 0 {
		s.websocket_rate_limit = 0.5
	}
	if s.websocket_burst == 0 {
		s.websocket_burst = 4
	}
	if s.delete_expired_events_period == 0 {
		s.delete_expired_events_period = time.Hour
	}
	return nil
}

func (s *Settings) UnmarshalJSON(payload []byte) error {
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
		case "host":
			sb, err := v.StringBytes()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'host' field: %w", err)
				return
			}
			s.host = string(sb)
		case "relay_url":
			sb, err := v.StringBytes()
			if err != nil {
				visiterr = fmt.Errorf("invalid 'relay_url' field: %w", err)
				return
			}
			s.relay_url = string(sb)
		case "nip11_info_document":
			s.nip11_info_document = make(json.RawMessage, 0)
			s.nip11_info_document = v.MarshalTo(s.nip11_info_document)
		case "max_limit":
			if i, e := v.Int(); e != nil {
				visiterr = fmt.Errorf("invalid 'max_limit' field: %w", err)
			} else {
				s.max_limit = i
			}
		case "subid_max_length":
			if i, e := v.Int(); e != nil {
				visiterr = fmt.Errorf("invalid 'subid_max_length' field: %w", err)
			} else {
				s.subid_max_length = i
			}
		case "websocket_rate_limit":
			if f, e := v.Float64(); e != nil {
				visiterr = fmt.Errorf("invalid 'websocket_rate_limit' field: %w", err)
			} else {
				s.websocket_rate_limit = rate.Limit(f)
			}
		case "websocket_burst":
			if i, e := v.Int(); e != nil {
				visiterr = fmt.Errorf("invalid 'websocket_burst' field: %w", err)
			} else {
				s.websocket_burst = i
			}
		case "delete_expired_events_period":
			if i, e := v.Int64(); e != nil {
				visiterr = fmt.Errorf("invalid 'delete_expired_events_period' field: %w", err)
			} else {
				s.delete_expired_events_period = time.Duration(i) * time.Second
			}
		}
	})
	if visiterr != nil {
		return visiterr
	}
	return nil
}
