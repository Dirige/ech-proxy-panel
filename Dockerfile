FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go index.html ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ech-workers .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai
COPY --from=builder /build/ech-workers /usr/local/bin/ech-workers
# 内置中国 IP 列表（两列格式：起始IP 结束IP，原版 loader 直接识别）
COPY chn_ip.txt /usr/local/bin/chn_ip.txt
COPY chn_ip_v6.txt /usr/local/bin/chn_ip_v6.txt
# 镜像内置配置：用户没有 /data/config.json 时兜底，保证开箱可用（用户自己的配置永远优先）
COPY config.example.json /etc/ech/config.json
ENTRYPOINT ["ech-workers"]
