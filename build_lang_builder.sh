docker buildx build \
  --platform linux/amd64 \
  -t lang-builder:prod \
  -f builder/Dockerfile \
  --load .