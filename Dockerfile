FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make server

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -h /app tunnel

COPY --from=builder /src/bin/tunnel-server /usr/local/bin/tunnel-server

RUN mkdir -p /etc/tunnel /var/lib/tunnel/certs && \
    chown -R tunnel:tunnel /var/lib/tunnel

USER tunnel
EXPOSE 7000 80 443

ENTRYPOINT ["tunnel-server"]
CMD ["--config", "/etc/tunnel/server.yaml"]
