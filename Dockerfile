FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY ech-workers.go index.html ./
RUN go mod init ech-workers && go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ech-workers ech-workers.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/ech-workers /usr/local/bin/ech-workers
# 内置中国 IP 列表（CIDR 格式，构建时已校验）
COPY chn_ip.txt /usr/local/bin/chn_ip.txt
COPY chn_ip_v6.txt /usr/local/bin/chn_ip_v6.txt
ENTRYPOINT ["ech-workers"]
