package main

import (
	"time"

	"golang.org/x/time/rate"
)

const relay_url = "localhost"
const nip11_info_document = "{\"contact\":\"barkyq\",\"description\":\"GNOST Relay\",\"name\":\"GNOST Relay\",\"pubkey\":\"\",\"software\":\"git+https://github.com/barkyq/gnost-relay\",\"supported_nips\":[4,9,11,12,15,16,20,22,28,33,42],\"version\":\"0.0\"}"

const max_limit = 25
const subid_max_length = 32
const websocket_rate_limit rate.Limit = 0.5
const websocket_burst = 4
const delete_expired_events_period = 10 * time.Minute
const read_buffer_size = 1024
