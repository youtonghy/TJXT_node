# Build go
FROM golang:1.26.1-alpine AS builder
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0
RUN GOEXPERIMENT=jsonv2 go mod download
RUN GOEXPERIMENT=jsonv2 go build -v -o tjxt-node .

# Release
FROM alpine
RUN apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN mkdir /etc/tjxt-node/
COPY --from=builder /app/tjxt-node /usr/local/bin

ENTRYPOINT [ "tjxt-node", "server", "--config", "/etc/tjxt-node/config.json"]
