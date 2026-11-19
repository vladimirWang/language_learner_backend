docker buildx build \
  --platform linux/amd64 \
  -t lang-service:prod \
  -f server/Dockerfile \
  --load .