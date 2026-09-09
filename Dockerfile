FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY ech-workers.go index.html ./
RUN go mod init ech-workers && go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ech-workers ech-workers.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/ech-workers /usr/local/bin/ech-workers
ENTRYPOINT ["ech-workers"]
