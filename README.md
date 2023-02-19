# gnost-relay
Nostr relay written in go

## Features
- NIPs supported: [9][9], [12][12], [15][15], [16][16], [20][20], [26][26], [28][28], [33][33], [40][40], [42][42].
- Websocket compression support (`permessage-deflate`), with support for client and server context takeover (i.e., sliding window support).
- Event handling logic happens via SQL triggers (i.e., NIPs [9][9], [16][16], [33][33], [40][40]).
- New events are notified to listeners via postgresql's built in `pg_notify` feature. This implies that different instances of `gnost-relay` can be run concurrently. Indeed, any software which writes to the DB will automatically notify listeners connected to the relay.
- Memory allocations are minimized using pools.
- Token bucket style rate limiters for handling the messages on each websocket connection.

[9]: https://github.com/nostr-protocol/nips/blob/master/09.md
[12]: https://github.com/nostr-protocol/nips/blob/master/12.md
[15]: https://github.com/nostr-protocol/nips/blob/master/15.md
[16]: https://github.com/nostr-protocol/nips/blob/master/16.md
[20]: https://github.com/nostr-protocol/nips/blob/master/20.md
[26]: https://github.com/nostr-protocol/nips/blob/master/26.md
[28]: https://github.com/nostr-protocol/nips/blob/master/28.md
[33]: https://github.com/nostr-protocol/nips/blob/master/33.md
[40]: https://github.com/nostr-protocol/nips/blob/master/40.md
[42]: https://github.com/nostr-protocol/nips/blob/master/42.md
