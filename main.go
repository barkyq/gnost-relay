package main

import (
	"bytes"
	"context"
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

	"golang.org/x/time/rate"
)

const RBS = 1024
const subid_max_length = 20

type EventSubmission struct {
	event  nostr.Event
	ctx    context.Context
	writer io.Writer
}
type ReqSubmission struct {
	addr    string
	id      string
	filters []ParsedFilter
	ctx     context.Context
	writer  io.Writer
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

	// generate the nip_11_bytes
	nip_11_bytes, err := NIP11_bytes()
	if err != nil {
		panic(err)
	}

	// start the listener
	ln, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		panic(err)
	}

	event_chan := make(chan EventSubmission)
	req_chan := make(chan ReqSubmission)
	close_chan := make(chan CloseSubmission)

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
					// upgrade successful! break to main websocket handler
					break
				}
			}
			// allocate stuff for websocket connection
			ctx, cancel := context.WithCancel(context.Background())
			payload := bytes_buf_pool.Get().(*bytes.Buffer)
			control := bytes_buf_pool.Get().(*bytes.Buffer)
			mask_buf := mask_buf_pool.Get().([]byte)
			json_msg := json_msg_pool.Get().([]json.RawMessage)

			defer func() {
				// cancel context and release buffers back to pool
				cancel()
				bytes_buf_pool.Put(payload)
				bytes_buf_pool.Put(control)
				mask_buf_pool.Put(mask_buf)
				json_msg_pool.Put(json_msg)
			}()

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
				case json_msg[0][1] == 'E':
					ev := nostr.Event{}
					if err := json.Unmarshal(json_msg[1], &ev); err != nil {
						frame := ws.NewTextFrame([]byte("[\"NOTICE\",\"Invalid EVENT\"]"))
						ws.WriteFrame(conn, frame)
						continue
					}
					if b, e := ev.CheckSignature(); e != nil || b != true {
						frame := ws.NewTextFrame([]byte(fmt.Sprintf("[\"OK\",\"%s\",false,\"\"]", ev.ID)))
						ws.WriteFrame(conn, frame)
						continue
					}
					event_chan <- EventSubmission{
						event:  ev,
						ctx:    ctx,
						writer: conn,
					}
				case json_msg[0][1] == 'R':
					filters := make([]ParsedFilter, 0)
					var id string
					var invalid bool
					if len(json_msg) < 3 {
						invalid = true
						goto jump
					}
					if err := json.Unmarshal(json_msg[1], &id); err != nil {
						invalid = true
						goto jump
					}
					for _, f := range json_msg[2:] {
						var filter ParsedFilter
						if err := json.Unmarshal(
							f,
							&filter,
						); err != nil {
							invalid = true
							goto jump
						}
						filters = append(filters, filter)
					}
				jump:
					if invalid || len(id) == 0 || len(id) > subid_max_length {
						var frame ws.Frame
						if invalid || len(id) == 0 {
							frame = ws.NewTextFrame([]byte("[\"NOTICE\",\"Invalid REQ message.\"]"))
						} else {
							frame = ws.NewTextFrame([]byte(fmt.Sprintf("[\"NOTICE\",\"Subscription ID %s is too long. Max length %d.\"]", id, subid_max_length)))
						}
						ws.WriteFrame(conn, frame)
						continue
					}
					req_chan <- ReqSubmission{
						addr:    conn.RemoteAddr().String(),
						id:      id,
						filters: filters,
						ctx:     ctx,
						writer:  conn,
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
			}
		}()
	}
}

func EventSubmissionHandler(event_chan chan EventSubmission, dbpool *pgxpool.Pool) {
	limiter := rate.NewLimiter(25, 5)
	for {
		ev := <-event_chan
		if e := limiter.Wait(ev.ctx); e == nil {
			if err := StoreEvent(ev.ctx, dbpool, ev.event); err != nil {
				panic(err)
			}
			ws.WriteFrame(ev.writer, ws.NewTextFrame([]byte(fmt.Sprintf("[\"OK\",\"%s\",true,\"\"]", ev.event.ID))))
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
							panic(err)
						}
						break
					}
				}
			}
			mu.Unlock()
		}
	}()

	// handler for incoming reqs.
	// allocations:
	buf := bytes.NewBuffer(nil)
	raw := new(json.RawMessage)
	for {
		select {
		case req := <-req_chan:
			if e := limiter.Wait(req.ctx); e == nil {
				dbconn, e := dbpool.Acquire(req.ctx)
				if e != nil {
					panic(e)
				}
				query, e := req.SQL()
				if e != nil {
					panic(e)
				}
				rows, e := dbconn.Query(req.ctx, query)
				if e != nil {
					panic(e)
				}
				for rows.Next() {
					buf.Reset()
					buf.Write([]byte(fmt.Sprintf("[\"EVENT\",\"%s\",", req.id)))
					if err := rows.Scan(raw); err != nil || raw == nil {
						panic(err)
					}
					buf.Write(*raw)
					buf.WriteByte(']')
					if err := ws.WriteFrame(req.writer, ws.NewTextFrame(buf.Bytes())); err != nil {
						panic(err)
					}
				}
				dbconn.Release()
				buf.Reset()
				buf.Write([]byte(fmt.Sprintf("[\"EOSE\",\"%s\"]", req.id)))
				if err := ws.WriteFrame(req.writer, ws.NewTextFrame(buf.Bytes())); err != nil {
					panic(err)
				}
				uid := req.addr + "/" + req.id
				fmt.Println(req.addr+"/"+req.id, "added")
				mu.Lock()
				subids[uid] = req
				mu.Unlock()
			}
		case close := <-close_chan:
			limiter.Wait(close.ctx)
			if len(close.id) > subid_max_length {
				continue
			}
			uid := close.addr + "/" + close.id
			fmt.Println(uid, "deleted")
			mu.Lock()
			delete(subids, uid)
			mu.Unlock()
		}
	}
}
