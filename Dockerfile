FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git curl
WORKDIR /build
COPY ech-workers.go index.html ./
RUN go mod init ech-workers && go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ech-workers ech-workers.go
# 在构建阶段（GitHub Actions，网络干净）下载中国 IP 列表并打包进镜像，
# 避免运行环境下载被中间设备篡改
RUN curl -fsSL -o chn_ip.txt "https://raw.githubusercontent.com/mayaxcn/china-ip-list/refs/heads/master/chn_ip.txt" || touch chn_ip.txt
RUN curl -fsSL -o chn_ip_v6.txt "https://raw.githubusercontent.com/mayaxcn/china-ip-list/refs/heads/master/chn_ip_v6.txt" || touch chn_ip_v6.txt

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/ech-workers /usr/local/bin/ech-workers
COPY --from=builder /build/chn_ip.txt /usr/local/bin/chn_ip.txt
COPY --from=builder /build/chn_ip_v6.txt /usr/local/bin/chn_ip_v6.txt
ENTRYPOINT ["ech-workers"]
