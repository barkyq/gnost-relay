package main

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"time"

	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"sync"
	"syscall"

	"github.com/gobwas/ws"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip42"

	"golang.org/x/time/rate"
)

const RBS = 1024
const subid_max_length = 20
const websocket_rate_limit rate.Limit = 0.5 // number of payloads (EVENT, REQ, CLOSE) per second
const websocket_burst = 4
const relay_url = "localhost"

type EventSubmission struct {
	event  nostr.Event
	ctx    context.Context
	writer io.Writer
	cancel context.CancelFunc
}
type ReqSubmission struct {
	addr    string
	id      string
	filters []ParsedFilter
	ctx     context.Context
	writer  io.Writer
	cancel  context.CancelFunc
}
type CloseSubmission struct {
	addr string
	id   string
	ctx  context.Context
}

func main() {
	// initilize db
	dbpool, err := InitStorage()
	if err != nil {
		panic(err)
	}

	// initialize the pools
	var bytes_buf_pool = sync.Pool{
		New: func() any {
			fmt.Println("new bytes buf!")
			return new(bytes.Buffer)
		},
	}
	var mask_buf_pool = sync.Pool{
		New: func() any {
			fmt.Println("new mask buf!")
			return make([]byte, RBS)
		},
	}
	var json_msg_pool = sync.Pool{
		New: func() any {
			fmt.Println("new json msg!")
			return make([]json.RawMessage, 0)
		},
	}

	// NIP_11_bytes. Read Only so don't need Mutex
	nip_11_bytes, err := NIP11_bytes()
	if err != nil {
		panic(err)
	}

	// for generating NIP-42 AUTH challenges
	// rand_reader is not safe for concurrent use
	var rand_mu sync.Mutex
	rand_reader := rand.New(rand.NewSource(time.Now().UnixNano() + int64(os.Getpid())))
	var challenge_bytes [16]byte

	// start the listener
	ln, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		panic(err)
	}

	event_chan := make(chan EventSubmission, 64)
	req_chan := make(chan ReqSubmission, 64)
	close_chan := make(chan CloseSubmission, 64)

	// start handlers
	go EventSubmissionHandler(event_chan, dbpool)
	go ReqSubmissionHandler(req_chan, close_chan, dbpool)

	// nip11 hijacker / websocket upgrader
	upgrader := ws.Upgrader{
		OnHeader: NIP11_hijack_header,
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			panic(err)
		}

		// one go routine per conn
		go func() {
			defer conn.Close()
			// pre-upgrade handler
			for {
				if _, err = upgrader.Upgrade(conn); err != nil {
					// error in upgrade. check if hijacked.
					if c, ok := err.(*ws.ConnectionRejectedError); ok == true {
						if c.StatusCode() == http.StatusTeapot {
							conn.Write(nip_11_bytes)
							// listen for more GET requests on this connection.
							continue
						}
					} else {
						// error in upgrade, and was not hijacked by NIP11, so close and continue
						return
					}
				} else {
					// Upgrade successful! Break to main websocket handler.
					break
				}
			}
			// allocate stuff for websocket connection
			ctx, cancel := context.WithCancel(context.Background())
			payload := bytes_buf_pool.Get().(*bytes.Buffer)
			control := bytes_buf_pool.Get().(*bytes.Buffer)
			mask_buf := mask_buf_pool.Get().([]byte)
			json_msg := json_msg_pool.Get().([]json.RawMessage)
			limiter := rate.NewLimiter(websocket_rate_limit, websocket_burst)

			defer func() {
				// cancel context and release buffers back to pool
				cancel()
				bytes_buf_pool.Put(payload)
				bytes_buf_pool.Put(control)
				mask_buf_pool.Put(mask_buf)
				json_msg_pool.Put(json_msg)
			}()

			// generate websocket_id
			rand_mu.Lock()
			rand_reader.Read(challenge_bytes[:])
			websocket_id := base64.StdEncoding.EncodeToString(challenge_bytes[:])
			rand_mu.Unlock()
			var authenticated_user string

			// send NIP-42 challenge
			if err := ws.WriteFrame(conn, ws.NewTextFrame([]byte(fmt.Sprintf("[\"AUTH\",\"%s\"]", websocket_id)))); err != nil {
				return
			}

			// post upgrade handler
			for {
				header, err := ws.ReadHeader(conn)
				switch {
				case err == nil:
				case errors.Is(err, io.EOF):
					fmt.Println("connection closed 1", conn.RemoteAddr())
					return
				case errors.Is(err, syscall.ECONNRESET):
					fmt.Println("connection closed 2", conn.RemoteAddr())
					return
				default:
					fmt.Println("unknown error", conn.RemoteAddr(), err.Error())
					return
				}
				// reset payload or control buffer
				if (header.OpCode != ws.OpContinuation) && header.OpCode.IsData() {
					if payload.Len() != 0 {
						payload.Reset()
					}
				} else if header.OpCode.IsControl() {
					if control.Len() != 0 {
						control.Reset()
					}
				}
				remaining := header.Length

				// get a mask_buf buffer. the mask changes per frame
				for remaining > 0 {
					var n int
					if remaining > RBS {
						n, _ = conn.Read(mask_buf[:])
					} else {
						n, _ = conn.Read(mask_buf[:remaining])
					}
					remaining -= int64(n)
					if header.Masked {
						ws.Cipher(mask_buf[:n], header.Mask, 0)
					}
					// control frames can be injected
					switch {
					case header.OpCode.IsControl():
						control.Write(mask_buf[:n])
					default:
						payload.Write(mask_buf[:n])
					}
				}

				//Handle Continuation
				if !header.Fin {
					continue
				}

				//Handle Control
				if header.OpCode.IsControl() {
					switch header.OpCode {
					case ws.OpClose:
						var b [2]byte // status code
						io.ReadFull(control, b[:])
						code := ws.StatusCode(uint16(b[0])*256 + uint16(b[1]))
						var frame ws.Frame
						if code.IsProtocolDefined() {
							frame = ws.NewCloseFrame(ws.NewCloseFrameBody(code, ""))
						} else {
							frame = ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusProtocolError, ""))
						}
						ws.WriteFrame(conn, frame)
						fmt.Println("connection closed 0", conn.RemoteAddr())
						return
					case ws.OpPing:
						ctrl_bytes, _ := io.ReadAll(control)
						frame := ws.NewPongFrame(ctrl_bytes)
						ws.WriteFrame(conn, frame)
					}
					continue
				}

				//Handle Payload
				if err := json.NewDecoder(payload).Decode(&json_msg); err != nil {
					notice := "[\"NOTICE\",\"Invalid Message\"]"
					frame := ws.NewTextFrame([]byte(fmt.Sprintf(notice, json_msg[1])))
					ws.WriteFrame(conn, frame)
					continue
				}
				switch {
				case json_msg[0][1] == 'A':
					ev := nostr.Event{}
					if err := json.Unmarshal(json_msg[1], &ev); err != nil {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"Invalid AUTH\"]"))
						if err = ws.WriteFrame(conn, frame); err != nil {
							return
						}
						// break hits the rate limiter
						break
					}
					if pub, ok := nip42.ValidateAuthEvent(&ev, websocket_id, relay_url); ok == true {
						authenticated_user = pub
						frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Authenticated as %s\"]", pub)))
						if err := ws.WriteFrame(conn, frame); err != nil {
							return
						}
						// continue skips the rate limiter
						continue
					} else {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"Authentication failed!\"]"))
						if err := ws.WriteFrame(conn, frame); err != nil {
							return
						}
						// End of block acts as break. Will hit the rate limiter
					}
				case json_msg[0][1] == 'E':
					ev := nostr.Event{}
					if err := json.Unmarshal(json_msg[1], &ev); err != nil {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"Invalid EVENT\"]"))
						ws.WriteFrame(conn, frame)
						break
					}
					if b, e := ev.CheckSignature(); e != nil || b != true {
						frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"OK\",\"%s\",false,\"\"]", ev.ID)))
						ws.WriteFrame(conn, frame)
						break
					}
					event_chan <- EventSubmission{
						event:  ev,
						ctx:    ctx,
						writer: conn,
						cancel: cancel,
					}
				case json_msg[0][1] == 'R':
					filters := make([]ParsedFilter, 0)
					var id string
					if len(json_msg) < 3 {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"REQ message too short.\"]"))
						if e := ws.WriteFrame(conn, frame); e != nil {
							return
						}
						break
					}
					if err := json.Unmarshal(json_msg[1], &id); err != nil {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"Cannot parse REQ message.\"]"))
						if e := ws.WriteFrame(conn, frame); e != nil {
							return
						}
						break
					}
					if len(id) > subid_max_length {
						frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Subscription ID %s is too long. Max length %d.\"]", id, subid_max_length)))
						if e := ws.WriteFrame(conn, frame); e != nil {
							return
						}
						break
					}
					// parse all the filters and discard any invalid ones
					for _, f := range json_msg[2:] {
						var filter ParsedFilter
						if err := json.Unmarshal(
							f,
							&filter,
						); err != nil {
							frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Invalid filter in %s: %s.\"]", id, err.Error())))
							if e := ws.WriteFrame(conn, frame); e != nil {
								return
							}
							goto skip
						}
						if len(filter.Kinds) == 0 {
							filter.Kinds = []int{1}
							// we know there is no kind 4
							// jump straight to append
							goto append
						}
						for _, k := range filter.Kinds {
							if k == 4 {
								switch {
								case authenticated_user != "" && len(filter.Authors) == 1 && authenticated_user == filter.Authors[0]:
									// sole author is authenticated user
									goto append
								case authenticated_user != "" && len(filter.Ptags) == 1 && authenticated_user == filter.Ptags[0]:
									// sole receiver is authenticated user
									goto append
								default:
									frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Invalid filter in %s: user is not authenticated as sender or receiver.\"]", id)))
									if e := ws.WriteFrame(conn, frame); e != nil {
										return
									}
									goto skip
								}
							}
						}
					append:
						filters = append(filters, filter)
					skip:
					}
					if len(filters) == 0 {
						frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"No filters were accepted. REQ Cancelled.\"]")))
						if e := ws.WriteFrame(conn, frame); e != nil {
							return
						}
						break
					}
					req_chan <- ReqSubmission{
						addr:    conn.RemoteAddr().String(),
						id:      id,
						filters: filters,
						ctx:     ctx,
						writer:  conn,
						cancel:  cancel,
					}
				case json_msg[0][1] == 'C':
					var id string
					var invalid bool
					if len(json_msg) < 2 {
						invalid = true
					} else {
						err := json.Unmarshal(json_msg[1], &id)
						invalid = err != nil
					}
					if invalid || len(id) == 0 || len(id) > subid_max_length {
						var frame ws.Frame
						if invalid || len(id) == 0 {
							frame = ws.NewTextFrame([]byte("[\"NOTICE\",\"Invalid CLOSE message.\"]"))
						} else {
							frame = ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Subscription ID %s is too long. Max length %d.\"]", id, subid_max_length)))
						}
						ws.WriteFrame(conn, frame)
						continue
					}
					close_chan <- CloseSubmission{
						addr: conn.RemoteAddr().String(),
						id:   id,
						ctx:  ctx,
					}
				}
				if e := limiter.Wait(ctx); e != nil {
					fmt.Println("connection closed 3", conn.RemoteAddr())
					return
				}
			}
		}()
	}
}

func EventSubmissionHandler(event_chan chan EventSubmission, dbpool *pgxpool.Pool) {
	limiter := rate.NewLimiter(25, 5)
	dbconn, e := dbpool.Acquire(context.Background())
	if e != nil {
		panic(e)
	}
	for {
		ev := <-event_chan
		if e := limiter.Wait(ev.ctx); e == nil {
			if e := ev.StoreEvent(dbconn); e != nil {
				panic(e)
			}
			if e := ws.WriteFrame(ev.writer, ws.NewTextFrame([]byte(fmt.Sprintf("[\"OK\",\"%s\",true,\"\"]", ev.event.ID)))); e != nil {
				ev.cancel()
			}
		}
	}
}

func ReqSubmissionHandler(req_chan chan ReqSubmission, close_chan chan CloseSubmission, dbpool *pgxpool.Pool) {
	// rate limiter for receiving req
	limiter := rate.NewLimiter(25, 5)
	// mutex for the subids map
	var mu sync.Mutex
	subids := make(map[string]ReqSubmission)

	// handler for events newly added to DB.
	go func() {
		var new_conn *pgxpool.Conn
		if c, err := dbpool.Acquire(context.Background()); err == nil {
			new_conn = c
		} else {
			panic(err)
		}
		newsub := new(DBNotification)
		buf := bytes.NewBuffer(nil)
		if _, err := new_conn.Exec(context.Background(), "listen submissions"); err != nil {
			return
		}
		for {
			if notification, err := new_conn.Conn().WaitForNotification(context.Background()); err == nil {
				if err = json.Unmarshal([]byte(notification.Payload), newsub); err != nil {
					panic(err)
				}
			} else {
				panic(err)
			}
			mu.Lock()
			for sid, req := range subids {
				if req.ctx.Err() != nil {
					delete(subids, sid)
					continue
				}
				for _, f := range req.filters {
					if f.Accept(newsub) {
						buf.Reset()
						buf.Write([]byte(fmt.Sprintf("[\"EVENT\",\"%s\",", req.id)))
						buf.Write(newsub.Raw)
						buf.WriteByte(']')
						if err := ws.WriteFrame(req.writer, ws.NewTextFrame(buf.Bytes())); err != nil {
							req.cancel()
						}
						break
					}
				}
			}
			mu.Unlock()
		}
	}()

	// to prevent SQL injections
	sql_dollar_quote := func() string {
		var b [20]byte
		rand.New(rand.NewSource(time.Now().UnixNano() + int64(os.Getpid()))).Read(b[:])
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
	}()

	// handler for incoming reqs.
	// allocations:
	buf := bytes.NewBuffer(nil)
	pf_buf := make([]ParsedFilter, 0)
	raw := new(json.RawMessage)
	for {
		select {
		case req := <-req_chan:
			if e := limiter.Wait(req.ctx); e == nil {
				dbconn, e := dbpool.Acquire(req.ctx)
				if e != nil {
					panic(e)
				}
				func() {
					defer dbconn.Release()
					query, e := req.SQL(sql_dollar_quote)
					if e != nil {
						panic(e)
					}
					rows, e := dbconn.Query(req.ctx, query)
					if e != nil {
						panic(e)
					}
					for rows.Next() {
						if req.ctx.Err() != nil {
							return
						}
						buf.Reset()
						buf.Write([]byte(fmt.Sprintf("[\"EVENT\",\"%s\",", req.id)))
						if err := rows.Scan(raw); err != nil || raw == nil {
							panic(err)
						}
						buf.Write(*raw)
						buf.WriteByte(']')
						if err := ws.WriteFrame(req.writer, ws.NewTextFrame(buf.Bytes())); err != nil {
							req.cancel()
							return
						}
					}
					buf.Reset()
					buf.Write([]byte(fmt.Sprintf("[\"EOSE\",\"%s\"]", req.id)))
					if err := ws.WriteFrame(req.writer, ws.NewTextFrame(buf.Bytes())); err != nil {
						req.cancel()
						return
					}
					uid := req.addr + "/" + req.id
					if err := req.Cull(pf_buf); err != nil {
						fmt.Println(uid, "culled!")
						return
					}
					fmt.Println(uid, "added!")
					mu.Lock()
					subids[uid] = req
					mu.Unlock()
				}()
			}
		case close := <-close_chan:
			limiter.Wait(close.ctx)
			if len(close.id) > subid_max_length {
				continue
			}
			uid := close.addr + "/" + close.id
			fmt.Println(uid, "deleted!")
			mu.Lock()
			delete(subids, uid)
			mu.Unlock()
		}
	}
}
