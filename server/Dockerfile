# 构建阶段
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/version-service ./cmd/version-service

# 运行阶段
FROM alpine:3.20
# git / openssh-client 用于 git ls-remote 查询素材仓库最新 commit
RUN apk add --no-cache ca-certificates tzdata git openssh-client
WORKDIR /app
COPY --from=builder /out/version-service ./version-service
EXPOSE 8080
ENTRYPOINT ["/app/version-service"]
