# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /src/basicroute .

FROM scratch
COPY --from=builder /src/basicroute /basicroute
COPY --from=builder /src/config.jsonc /config.jsonc
EXPOSE 21172
ENTRYPOINT ["/basicroute"]
