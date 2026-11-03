# tunnel

A self-hosted reverse tunnel. Expose any localhost port to the public internet through your own server. No account required, no rate limits, no vendor lock-in.

## How it works

```
┌──────────┐         TLS / yamux          ┌──────────────┐        HTTPS
│  tunnel   │ ◄──────────────────────────► │ tunnel-server │ ◄──────────── internet
│  (local)  │    persistent connection     │   (your VPS)  │
└──────────┘                               └──────────────┘
localhost:3000                              swift-arch-4f2a.tunnel.yourdomain.com
```

The client opens a persistent multiplexed connection (yamux over TLS) to your server. When traffic hits the server on a subdomain, it's forwarded through that connection to your local port. Response flows back the same way.

## Self-hosting in 8 steps

**1.** Get a VPS with a public IP and a domain. Point a wildcard DNS record at it:

```
*.tunnel.yourdomain.com  →  A  →  your.vps.ip
tunnel.yourdomain.com    →  A  →  your.vps.ip
```

**2.** Clone this repo on the server:

```
git clone https://github.com/nopedal/gotunnel.git && cd gotunnel
```

**3.** Copy the example config and set your domain and a secret token:

```
cp server.yaml /etc/tunnel/server.yaml
```

```yaml
domain: tunnel.yourdomain.com
token: $(openssl rand -hex 32)
control_port: 7000
http_port: 80
https_port: 443
max_clients: 50
tls_enabled: true
```

**4.** Run with Docker Compose:

```
docker compose up -d
```

Or build from source:

```
make build
./bin/tunnel-server --config /etc/tunnel/server.yaml
```

**5.** On your local machine, install the client:

```
go install github.com/nopedal/gotunnel/cmd/tunnel@latest
```

Or build it from a clone:

```
make client
sudo mv bin/tunnel /usr/local/bin/
```

**6.** (Optional) Save your defaults so you don't need flags every time:

```yaml
# ~/.tunnel/config.yaml
server: tunnel.yourdomain.com
token: your-secret-token
```

**7.** Connect:

```
tunnel connect --server tunnel.yourdomain.com --token YOUR_TOKEN --port 3000
```

**8.** Share the URL printed in your terminal. Done.

## Usage

```
# Expose a local web server
tunnel connect --server tunnel.yourdomain.com --port 3000

# Request a specific subdomain
tunnel connect --server tunnel.yourdomain.com --port 8080 --subdomain myapp

# TCP mode (databases, game servers, etc.)
tunnel connect --server tunnel.yourdomain.com --port 5432 --mode tcp

# Token via environment variable
export TUNNEL_TOKEN=your-secret-token
tunnel connect --server tunnel.yourdomain.com --port 3000
```

## Server API

| Endpoint | Description |
|---|---|
| `GET /health` | Server health check |
| `GET /api/tunnels` | List active tunnels |

## Building

```
make build          # build both binaries
make cross          # cross-compile for linux/amd64, darwin/arm64, darwin/amd64
make docker         # build Docker image
make clean          # remove build artifacts
```

## Architecture

```
cmd/tunnel-server/     server entrypoint
cmd/tunnel/            client entrypoint
internal/server/       server logic, config, HTTP routing, TLS
internal/client/       client logic, reconnection, request forwarding
internal/proto/        wire protocol (typed messages over length-prefixed frames)
internal/tunnel/       shared: yamux config, tunnel registry, slug generation
```

## License

MIT
