module github.com/wthomson/ctrlfreak

go 1.23

// Direct dependencies. Run `go mod tidy` once on a machine with internet to
// resolve indirect deps and generate go.sum (the build scripts do this for you).
require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/kardianos/service v1.2.2
	github.com/kbinani/screenshot v0.0.0-20230812210009-b87d31814237
	github.com/pion/webrtc/v4 v4.0.9
	golang.org/x/crypto v0.31.0
	golang.org/x/image v0.23.0
	golang.org/x/sys v0.28.0
	golang.org/x/term v0.27.0
	modernc.org/sqlite v1.34.4
)
