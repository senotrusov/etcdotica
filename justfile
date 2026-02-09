prompt *args:
  prompt-collect-files go.mod justfile README.md cmd/etcdotica/*.go {{args}}

static:
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o build/etcdotica ./cmd/etcdotica

cross-platform:
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o build/etcdotica.exe ./cmd/etcdotica

local-install: static
  sudo install --compare --mode 0755 --owner root --group root --target-directory /usr/local/bin build/etcdotica

format:
  mdformat README.md
