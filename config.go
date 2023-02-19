package main

import (
	"time"

	"golang.org/x/time/rate"
)

const relay_url = "ws://localhost"
const nip11_info_document = "{\"name\":\"GNOST Relay\",\"description\":\"GNOST Relay\",\"pubkey\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"contact\":\"barkyq\",\"supported_nips\":[9,12,15,16,20,22,26,28,33,42],\"software\":\"git+https://github.com/barkyq/gnost-relay\",\"version\":\"0.2\"}"

const max_limit = 25
const subid_max_length = 32
const websocket_rate_limit rate.Limit = 0.5 // 1/rate number of seconds wait per trigger of the rate limiter
const websocket_burst = 4
const delete_expired_events_period = 60 * time.Minute
