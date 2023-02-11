package main

import (
	"time"

	"golang.org/x/time/rate"
)

const max_limit = 25
const subid_max_length = 20
const websocket_rate_limit rate.Limit = 0.5
const websocket_burst = 4
const relay_url = "localhost"
const delete_expired_events_period = 10 * time.Minute
const read_buffer_size = 1024
