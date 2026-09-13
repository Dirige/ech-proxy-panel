FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod ./
COPY ech-workers.go index.html ./
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ech-workers ech-workers.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/ech-workers /usr/local/bin/ech-workers
# 内置中国 IP 列表（CIDR 格式，构建时已校验）
COPY chn_ip.txt /usr/local/bin/chn_ip.txt
COPY chn_ip_v6.txt /usr/local/bin/chn_ip_v6.txt
VOLUME ["/data"]
EXPOSE 30000 9090
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:9090/api/status >/dev/null 2>&1 || nc -z 127.0.0.1 30000 || exit 1
ENTRYPOINT ["ech-workers"]
# 默认同时启动 Web 面板（:9090），命令行参数可覆盖
CMD ["-web", ":9090"]
