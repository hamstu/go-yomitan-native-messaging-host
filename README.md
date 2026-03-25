# Yomitan Native Messaging Host (Go)

A Go implementation of the native messaging host for [Yomitan API](https://github.com/yomidevs/yomitan-api). It acts as a bridge between the Yomitan browser extension and local system resources, communicating over the browser's native messaging protocol (stdin/stdout) and exposing a local HTTP server on `127.0.0.1:19633`.

## Building

```bash
# Current platform
go build -o yomitan-host

# All platforms
./build.sh
```

## Installation

Build the binary, then run the built-in installer to register the native messaging host with your browser:

```bash
# Install for all detected browsers
./yomitan-host install

# Install for a specific browser
./yomitan-host install --browser firefox
```

Supported browsers: Firefox, Chrome, Chromium, Edge, Brave, Arc.

## Usage

The host is started automatically by the browser when the Yomitan extension needs it. It manages single-instance enforcement via a `.crowbar` PID file and logs errors to `error.log`.

## Testing

```bash
go test ./...
```
