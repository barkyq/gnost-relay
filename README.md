# gnost-relay
Nostr relay written in `go`

Still alpha and logging is done directly to stdout so it might be a bit messy to run currently. 

# features
Much of the event handling logic happens via SQL triggers, and new events are notified to listeners via postgresql's built in notification feature. This means that different instances of `gnost-relay` can be run concurrently.

Special care has been paid to reduce memory allocations to keep things lightweight. Token bucket style rate limiters have also been used when handling the messages on each websocket connection.
