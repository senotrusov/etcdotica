prompt *args:
  prompt-collect-files main.go README.md {{args}}

static:
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o etcdotica
