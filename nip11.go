package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
)

func NIP11_gzip_bytes() (doc []byte, gzip_doc []byte, err error) {
	bw := bytes.NewBuffer(nil)
	gzip_writer := gzip.NewWriter(bw)
	var gzip_document []byte
	var document []byte = []byte(nip11_info_document)
	if _, err := gzip_writer.Write(document); err != nil {
		return nil, nil, err
	} else {
		if err := gzip_writer.Flush(); err != nil {
			panic(err)
		}
		gzip_document = make([]byte, bw.Len())
		if _, err := bw.Read(gzip_document); err != nil {
			panic(err)
		} else {
		}
	}
	if _, err := bw.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/gzip\r\n")); err != nil {
		return nil, nil, err
	}
	if _, err := bw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(gzip_document)))); err != nil {
		return nil, nil, err
	}
	if _, err := bw.Write([]byte("Connection: keep-alive\r\nAccess-Control-Allow-Origin: *\r\nContent-Encoding: gzip\r\n\r\n")); err != nil {
		return nil, nil, err
	}
	bw.Write(gzip_document)
	gzip_document = append(gzip_document[:0], bw.Bytes()...)

	bw.Reset()
	if _, err := bw.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n")); err != nil {
		return nil, nil, err
	}
	if _, err := bw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(document)))); err != nil {
		return nil, nil, err
	}
	if _, err := bw.Write([]byte("Connection: keep-alive\r\nAccess-Control-Allow-Origin: *\r\n\r\n")); err != nil {
		return nil, nil, err
	}
	if _, err := bw.Write(document); err != nil {
		return nil, nil, err
	}
	document = append(document[:0], bw.Bytes()...)
	return document, gzip_document, nil
}

func NIP11_hijack_header(err error, key, value []byte) error {
	var b [10]byte
	copy(b[:], key)
	switch b {
	case [10]byte{'A', 'c', 'c', 'e', 'p', 't'}:
		target_value := []byte("application/nostr+json")
		if len(value) > 22 {
			return nil
		}
		for i, b := range value {
			if target_value[i] != b {
				return nil
			}
		}
		if e, ok := err.(*nip11_escape); ok {
			e.escape = true
		} else {
			err = &nip11_escape{true, [4]byte{}}
		}
	case [10]byte{'A', 'c', 'c', 'e', 'p', 't', '-', 'E', 'n', 'c'}:
		if e, ok := err.(*nip11_escape); ok {
			copy(e.encoding[:], value)
		} else {
			e = &nip11_escape{}
			copy(e.encoding[:], value)
			err = e
		}
	}
	return err
}

type nip11_escape struct {
	escape   bool
	encoding [4]byte
}

func (err *nip11_escape) Error() string {
	return "nip11 escape error"
}

func (err *nip11_escape) Escape() bool {
	return err.escape
}
