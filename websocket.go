package main

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
)

const read_buffer_size = 1024

var flush_bytes = [4]byte{0x00, 0x00, 0xff, 0xff}

func handle_websocket(handshake *ws.Handshake, bytes_buf_pool, flate_reader_pool, flate_writer_pool, mask_buf_pool, msg_pool *sync.Pool, conn net.Conn, logger *log.Logger) (msgs chan *Message, writer io.WriteCloser) {
	var permessage_deflate, server_nct, client_nct bool
	for _, opt := range handshake.Extensions {
		target := []byte("permessage-deflate")
		if len(opt.Name) != len(target) {
			continue
		}
		permessage_deflate = true
		for i, b := range opt.Name {
			if b != target[i] {
				goto jump
			}
		}
		_, client_nct = opt.Parameters.Get("client_no_context_takeover")
		_, server_nct = opt.Parameters.Get("server_no_context_takeover")
	jump:
	}
	msgs = make(chan *Message)

	// data to writer is compressed (if negotiated) and sent to reader
	var reader io.Reader // data to reader is framed and sent to client websocket connection

	// compression handler
	if permessage_deflate {
		rp, wp := io.Pipe()
		rpp, wpp := io.Pipe()
		reader = rpp
		writer = wp
		go func() {
			flate_writer := flate_writer_pool.Get().(*flate.Writer)
			buf := mask_buf_pool.Get().([]byte)
			defer mask_buf_pool.Put(buf)
			defer flate_writer_pool.Put(flate_writer)
			flate_writer.Reset(wpp)
			for {
				for {
					n, e := rp.Read(buf)
					switch {
					case e == nil:
					case e == io.EOF:
						wpp.Close()
						return
					default:
						panic(e)
					}
					if n == 4 && buf[0] == 0x00 && buf[1] == 0x00 && buf[2] == 0xff && buf[3] == 0xff {
						switch {
						case server_nct:
							e = flate_writer.Close()
							flate_writer.Reset(wpp)
						default:
							flate_writer.Flush()
						}
						break
					}
					if _, e = flate_writer.Write(buf[:n]); e != nil {
						panic(e)
					}
				}
			}
		}()
	} else {
		// wp -> rp copy
		rp, wp := io.Pipe()
		reader = rp
		writer = wp
	}

	// sending handler
	go func() {
		payload := bytes_buf_pool.Get().(*bytes.Buffer)
		buf := mask_buf_pool.Get().([]byte)
		defer bytes_buf_pool.Put(payload)
		defer mask_buf_pool.Put(buf)
		for {
			for {
				n, e := reader.Read(buf)
				switch {
				case e == nil:
				case e == io.EOF:
					if e := ws.WriteFrame(conn, ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))); e != nil {
						panic(e)
					}
					time.Sleep(10 * time.Second)
					if cl, ok := conn.(io.Closer); ok == true {
						cl.Close()
					}
					return
				default:
					panic(e)
				}
				// test for flush
				// no edge case, since there will never be a valid send of an empty uncompressed block
				if n == 4 && buf[0] == 0x00 && buf[1] == 0x00 && buf[2] == 0xff && buf[3] == 0xff {
					break
				}
				payload.Write(buf[:n])
			}
			fr := ws.NewTextFrame(payload.Bytes())
			if permessage_deflate {
				fr.Header.Rsv = ws.Rsv(true, false, false)
			}
			ws.WriteFrame(conn, fr)
			payload.Reset()
		}
	}()

	// receiving handler
	go func() {
		payload := bytes_buf_pool.Get().(*bytes.Buffer)
		control := bytes_buf_pool.Get().(*bytes.Buffer)
		mask_buf := mask_buf_pool.Get().([]byte)
		defer mask_buf_pool.Put(mask_buf)
		defer bytes_buf_pool.Put(payload)
		defer bytes_buf_pool.Put(control)
		var decoder, fr_decoder *json.Decoder
		var fr io.Reader
		// may need to use this, even if permessage_deflate has been negotiated
		// so allocate:
		payload_decoder := json.NewDecoder(payload)

		if permessage_deflate {
			// only allocate these if needed
			// flate package does not have good hooks for memory allocation
			fr := flate_reader_pool.Get().(io.ReadCloser)
			defer flate_reader_pool.Put(fr)
			if rs, ok := fr.(flate.Resetter); ok == true {
				if err := rs.Reset(payload, nil); err != nil {
					panic(err)
				}
			} else {
				panic("flate reader reset error")
			}
			fr_decoder = json.NewDecoder(fr)
		}
		var compressed bool
		for {
			header, err := ws.ReadHeader(conn)
			switch {
			case err == nil:
			case errors.Is(err, io.EOF):
				logger.Println("connection closed", conn.RemoteAddr())
				return
			case errors.Is(err, syscall.ECONNRESET):
				logger.Println("connection closed", conn.RemoteAddr())
				return
			default:
				logger.Println("unknown error", conn.RemoteAddr(), err.Error())
				return
			}

			// reset payload or control buffer
			if (header.OpCode != ws.OpContinuation) && header.OpCode.IsData() {
				compressed = header.Rsv1()
				payload.Reset()
			} else if header.OpCode.IsControl() {
				control.Reset()
			}

			// unmask the frame payload
			remaining := header.Length
			for remaining > 0 {
				var n int
				if remaining > read_buffer_size {
					n, err = conn.Read(mask_buf[:])
				} else {
					n, err = conn.Read(mask_buf[:remaining])
				}
				if err != nil {
					panic(err)
				}
				remaining -= int64(n)
				if header.Masked {
					ws.Cipher(mask_buf[:n], header.Mask, 0)
				}

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
					control.Read(b[:])
					code := ws.StatusCode(uint16(b[0])*256 + uint16(b[1]))
					var frame ws.Frame
					if code.IsProtocolDefined() {
						frame = ws.NewCloseFrame(ws.NewCloseFrameBody(code, ""))
					} else {
						frame = ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusProtocolError, ""))
					}
					ws.WriteFrame(conn, frame)
					logger.Println("connection closed", conn.RemoteAddr())
					return
				case ws.OpPing:
					frame := ws.NewPongFrame(control.Bytes())
					ws.WriteFrame(conn, frame)
				}
				continue
			}

			if compressed {
				payload.Write([]byte{0x00, 0x00, 0xff, 0xff})
				decoder = fr_decoder
			} else {
				decoder = payload_decoder
			}

			jmsg := msg_pool.Get().([]json.RawMessage)
			if err := decoder.Decode(&jmsg); err != nil {
				panic(err)
			}
			msgs <- &Message{jmsg, msg_pool}
			if r, ok := fr.(flate.Resetter); client_nct && ok == true {
				r.Reset(payload, nil)
			}
		}
	}()
	return
}

func WS_upgrader() ws.Upgrader {
	// nip11 handler + websocket upgrader
	return ws.Upgrader{OnHeader: NIP11_hijack_header, Extension: negotiate}
}

var ws_upgrade_params = wsflate.Parameters{
	ServerNoContextTakeover: false, // default: server can reuse LZ77 buffer
	ClientNoContextTakeover: false, // default: client can reuse LZ77 buffer
}

func negotiate(opt httphead.Option) bool {
	var b [18]byte
	copy(b[:], opt.Name)
	if b != [18]byte{'p', 'e', 'r', 'm', 'e', 's', 's', 'a', 'g', 'e', '-', 'd', 'e', 'f', 'l', 'a', 't', 'e'} {
		return false
	}
	client_params := wsflate.Parameters{}
	if err := client_params.Parse(opt); err != nil {
		return false
	} else {
		switch {
		case client_params.ClientMaxWindowBits != ws_upgrade_params.ClientMaxWindowBits:
			return false
		case client_params.ServerMaxWindowBits != ws_upgrade_params.ServerMaxWindowBits:
			return false
		default:
			return true
		}
	}
}
