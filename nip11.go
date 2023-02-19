package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
)

type NIP11_document struct {
	document      []byte
	gzip_document []byte
}

func (doc *NIP11_document) Parse(nip11_document_bytes []byte) (err error) {
	bw := bytes.NewBuffer(nil)
	gzip_writer := gzip.NewWriter(bw)
	if _, err := gzip_writer.Write(nip11_document_bytes); err != nil {
		return err
	} else {
		if err := gzip_writer.Flush(); err != nil {
			return err
		}
		for len(doc.gzip_document) < bw.Len() {
			doc.gzip_document = append(doc.gzip_document, '0')
		}
		doc.gzip_document = doc.gzip_document[:bw.Len()]
		if _, err := bw.Read(doc.gzip_document); err != nil {
			return err
		}
	}
	bw.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/gzip\r\n"))
	bw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(doc.gzip_document))))
	bw.Write([]byte("Connection: keep-alive\r\nAccess-Control-Allow-Origin: *\r\nContent-Encoding: gzip\r\n\r\n"))
	bw.Write(doc.gzip_document)
	doc.gzip_document = append(doc.gzip_document[:0], bw.Bytes()...)

	bw.Reset()
	doc.document = append(doc.document[:0], nip11_document_bytes...)
	bw.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"))
	bw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(doc.document))))
	bw.Write([]byte("Connection: keep-alive\r\nAccess-Control-Allow-Origin: *\r\n\r\n"))
	if _, err = bw.Write(doc.document); err != nil {
		return err
	}
	doc.document = append(doc.document[:0], bw.Bytes()...)
	return nil
}

var target_value = []byte("application/nostr+json")

func NIP11_EscapeHatch(encoding []byte) func(error, []byte, []byte) error {
	var k [10]byte
	return func(err error, key, value []byte) error {
		k = [10]byte{}
		copy(k[:], key)
		switch k {
		case [10]byte{'A', 'c', 'c', 'e', 'p', 't'}:
			if len(value) > 22 {
				return nil
			}
			for i, b := range value {
				if target_value[i] != b {
					return nil
				}
			}
			return &nip11_escape{}
		case [10]byte{'A', 'c', 'c', 'e', 'p', 't', '-', 'E', 'n', 'c'}:
			copy(encoding, value)
		}
		return err
	}
}

type nip11_escape struct{}

func (err *nip11_escape) Error() string {
	return "nip11 escape error"
}

func (err *nip11_escape) Escape() bool {
	return true
}
