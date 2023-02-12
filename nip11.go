package main

import (
	"bytes"
	"fmt"

	"github.com/gobwas/ws"
)

func NIP11_bytes() ([]byte, error) {
	bw := bytes.NewBuffer(nil)
	if _, err := bw.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n")); err != nil {
		return nil, err
	}
	if _, err := bw.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(nip11_info_document)))); err != nil {
		return nil, err
	}
	if _, err := bw.Write([]byte("Connection: keep-alive\r\nAccess-Control-Allow-Origin: *\r\n\r\n")); err != nil {
		return nil, err
	}
	if _, err := bw.Write([]byte(nip11_info_document)); err != nil {
		return nil, err
	}
	return bw.Bytes(), nil
}

func NIP11_hijack_header(key, value []byte) error {
	target_key := []byte("Accept")
	if len(key) > 6 {
		return nil
	}
	for i, b := range key {
		if target_key[i] != b {
			return nil
		}
	}
	target_value := []byte("application/nostr+json")
	if len(value) > 22 {
		return nil
	}
	for i, b := range value {
		if target_value[i] != b {
			return nil
		}
	}
	return ws.ErrHandshakeHijack
}