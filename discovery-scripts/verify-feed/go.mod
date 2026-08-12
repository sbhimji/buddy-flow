module buddy-flow/scripts/verify-feed

go 1.21

require (
	github.com/joho/godotenv v1.5.1
	github.com/massive-com/client-go/v3 v3.0.0
	github.com/sirupsen/logrus v1.9.4
)

require (
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/exp v0.0.0-20220414153411-bcd21879b8fd // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.29.0 // indirect
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637 // indirect
)

replace github.com/massive-com/client-go/v3 => ../../../client-go
