prompt *args:
  prompt-collect-files main.go README.md {{args}}

static:
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o etcdotica

local-install: static
  sudo install --compare --mode 0755 --owner root --group root --target-directory /usr/local/bin etcdotica

format:
  mdformat README.md
