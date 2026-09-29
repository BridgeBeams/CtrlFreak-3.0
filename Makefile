# CtrlFreak build scripts (Linux/macOS). Requires Go 1.23+ and internet on first run.
#
#   make deps     # one-time: fetch dependencies, generate go.sum
#   make relay    # build the relay server for this machine
#   make host     # build the host agent for this machine
#   make windows  # cross-build both .exe files for Windows (amd64)
#   make linux    # cross-build both for Linux (amd64) e.g. the home server
#   make all      # everything into ./dist
#
# The host agent is pure Go (no cgo), so cross-compiling to Windows just works.

BIN := dist
LDFLAGS := -s -w

.PHONY: deps relay host windows linux all clean

deps:
	go mod tidy

relay: deps
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-relay ./cmd/relay

host: deps
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-host ./cmd/host

windows: deps
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-relay.exe ./cmd/relay
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-host.exe  ./cmd/host

linux: deps
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-relay-linux ./cmd/relay
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/ctrlfreak-host-linux  ./cmd/host

all: windows linux relay host
	@echo "Built into ./$(BIN):" && ls -1 $(BIN)

clean:
	rm -rf $(BIN)
