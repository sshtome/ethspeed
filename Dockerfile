# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ethspeed .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 app

WORKDIR /app
COPY --from=build /out/ethspeed /app/ethspeed
USER app

ENTRYPOINT ["/bin/sh","-c","exec ./ethspeed -mode server -host \"${HOST:-0.0.0.0}\" -port \"${PORT:-8080}\" \"$@\"","--"]
