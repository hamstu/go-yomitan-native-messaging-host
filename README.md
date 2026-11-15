# Yomitan Native Messaging Host (Go)

A Go implementation of the native messaging host for [Yomitan API](https://github.com/yomidevs/yomitan-api). It acts as a bridge between the Yomitan browser extension and local system resources, communicating over the browser's native messaging protocol (stdin/stdout) and exposing a local HTTP server on `127.0.0.1:19633`.

It is a Go port of upstream's `yomitan_api.py`, and it's used by [Kimochi](https://github.com/hamstu/kimochi-tauri).

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

On macOS and Linux, `install` copies the binary into each browser's native messaging hosts folder. The copy is written to a temp file in that folder and renamed over the old binary, so it is safe to run while the browser has the old host running: the running process keeps its file, and the folder never holds a half-written binary.

## Version

```bash
./yomitan-host version   # prints e.g. v3
```

`version` prints the release tag the binary was built as (`dev` for local builds) and exits. It starts no server and touches no files. Release builds set it with `-ldflags "-X main.hostVersion=vN"`.

Each release also attaches a `SHA256SUMS` file with one `sha256  filename` line per binary.

## Usage

The host is started automatically by the browser when the Yomitan extension needs it. It manages single-instance enforcement via a `.crowbar` PID file and logs errors to `error.log`.

## HTTP API

The host listens on `127.0.0.1:19633`. `POST /serverVersion` (or `POST /`) answers without contacting the extension:

```json
{"version":1,"host":"kimochi","hostVersion":"v3","concurrent":true}
```

- `version`: the Yomitan native messaging API version. Yomitan compares this, so it stays `1`.
- `host`: identifies this host build (as opposed to upstream's Python `yomitan_api.py`).
- `hostVersion`: the release tag, as printed by `yomitan-host version`.
- `concurrent`: `true` means requests are forwarded concurrently (see below).

Any other `POST /<action>` is forwarded to Yomitan as a native message; the query string becomes `params` and the request body becomes `body`.

### Concurrency

Requests are forwarded to Yomitan as they arrive rather than one at a time, so a slow request (an `ankiFields` call that downloads audio can take seconds) does not hold up fast ones such as `tokenize`. Each forwarded message carries a `_requestId` param; Yomitan echoes a request's params in its reply, and the host routes each reply to its caller by that id. Until a reply has echoed an id, the host keeps one request in flight and matches replies in order, as older versions did. A request with no reply after 30 seconds gets `504`.

## Testing

```bash
go test ./...
```

## Licence

GPL-3.0, the same as upstream [yomitan-api](https://github.com/yomidevs/yomitan-api). See [LICENSE](LICENSE).
