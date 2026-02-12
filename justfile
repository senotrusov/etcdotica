#  Copyright 2025-2026 Stanislav Senotrusov
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

# Set the project name
project := "etcdotica"

# Retrieve the GPG signing key from the Git configuration
signingkey := `git config --get user.signingkey`

# Get the nearest tag.
# If the commit matches the tag exactly, the output is "v1.1.1".
# If there are four commits after the tag, the output is
# "v1.1.1-4-gabc123", where "g" indicates a Git commit hash.
# If there are uncommitted changes, "-dirty" is added.
# If the repository is unreadable, "-broken" is added.
#
# Note that if there is a file that exists but is not added to Git,
# it will not be marked as dirty by `git describe`.
#
version := `
  tag=$(git describe --tags --always --dirty --broken) || {
    echo "Error: Failed to obtain git describe." >&2
    exit 1
  }

  status=$(git status --porcelain) || {
    echo "Error: Failed to obtain git status." >&2
    exit 1
  }

  if [ -n "$status" ]; then
    case "$tag" in
      *-dirty) ;; # Do nothing if tag already ends in -dirty
      *) tag="${tag}-dirty" ;; # Append the suffix
    esac
  fi

  echo "$tag"
`

# Format project files
format:
  mdformat README.md

# Gather source context for LLM prompts
context *args:
  prompt-collect-files go.mod justfile README.md cmd/{{project}}/*.go {{args}}

# Build and install the binary to /usr/local/bin
install: build
  sudo install --compare --mode 0755 --owner root --group root --target-directory /usr/local/bin bin/{{project}}

# Build the binary for the current OS/Arch
build:
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version={{version}}" -o bin/{{project}} ./cmd/{{project}}

# Remove all build artifacts
clean:
  rm -rf ./dist ./bin

# Remove dist artifacts
clean-dist:
  rm -rf ./dist

# Prepare a full release
release: ensure-release-tag dist

# Ensure repo is clean and strictly pointed to by an annotated tag
ensure-release-tag:
  #!/usr/bin/env sh
  # Get the current repository status to check for uncommitted changes
  status=$(git status --porcelain) || {
    echo "Error: Failed to obtain git status." >&2
    exit 1
  }

  # Fail if there are any modified, added, or deleted files in the working directory
  if [ -n "$status" ]; then
    echo "Error: Working directory is not clean. Commit your changes before releasing." >&2
    exit 1
  fi

  # Identify the exact annotated tag pointing to the current commit.
  # This will fail if the tag is lightweight or if we are not exactly on the tag.
  tag=$(git describe --dirty --exact-match) || {
    echo "Error: Current commit is not pointed to by an exact, annotated tag." >&2
    echo "Formal releases require an annotated tag (e.g., git tag -a v1.0.0)." >&2
    exit 1
  }

  # Ensure the tag does not include a "dirty" suffix from uncommitted changes
  case "$tag" in
    *-dirty)
      echo "Error: Working directory is dirty. Cannot release." >&2
      exit 1
      ;;
  esac

  # Final verification: Does the strict annotated tag match our project version?
  # A mismatch here usually means the version variable is using a lightweight tag 
  # or a commit hash that 'ensure-release-tag' refuses to accept.
  if [ "$tag" != "{{version}}" ]; then
    echo "Error: Version mismatch. Release requires a formal annotated tag." >&2
    echo "Current tag '$tag' does not match calculated version '{{version}}'." >&2
    exit 1
  fi

# Create distribution artifacts
dist: clean-dist cross-compile archive-source
  # Generate checksums and compress artifacts
  cd dist && find . -maxdepth 1 -type f ! -name '*.zst' -printf '%f\0' | LC_ALL=C sort -z | xargs -0 zstd --compress --ultra -20 --rm
  cd dist && find . -maxdepth 1 -type f ! -name 'SHA256SUMS' -printf '%f\0' | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS

  # Sign release artifacts
  cd dist && gpg --default-key "{{signingkey}}" --armor --detach-sign --output {{project}}-{{version}}.tar.zst.asc {{project}}-{{version}}.tar.zst
  cd dist && gpg --default-key "{{signingkey}}" --armor --detach-sign --output SHA256SUMS.asc SHA256SUMS

  # Verify integrity
  cd dist && sha256sum -c SHA256SUMS

  # Verify GPG signatures
  cd dist && gpg --verify {{project}}-{{version}}.tar.zst.asc {{project}}-{{version}}.tar.zst
  cd dist && gpg --verify SHA256SUMS.asc SHA256SUMS

# Create a tarball of the source code
archive-source:
  #!/usr/bin/env sh
  mkdir -p dist

  status=$(git status --porcelain) || {
    echo "Error: Failed to obtain git status." >&2
    exit 1
  }

  if [ -n "$status" ]; then
    echo "Creating archive from local files (uncommitted changes detected)..." >&2
    tar --exclude='./bin' --exclude='./.git' --exclude='./.gitignore' --exclude='./dist' --transform='s,^\.,{{project}}-{{version}},' --create --file="dist/{{project}}-{{version}}.tar" .
  else
    echo "Creating archive from git HEAD (repository is clean)..." >&2
    git archive --format=tar --prefix="{{project}}-{{version}}/" HEAD > dist/{{project}}-{{version}}.tar
  fi

# Cross compile for all platforms
cross-compile:
  #!/usr/bin/env sh
  # https://pkg.go.dev/internal/platform
  # https://go.dev/doc/install/source#environment
  target() {
    output="dist/{{project}}-{{version}}-$1-$2$3"
    echo "Compiling $output" >&2
    GOOS="$1" GOARCH="$2" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version={{version}}" -o "$output" ./cmd/{{project}}
  }
  # target aix ppc64
  # target android 386
  # target android amd64
  # target android arm
  # target android arm64
  target darwin amd64
  target darwin arm64
  target dragonfly amd64
  target freebsd 386
  target freebsd amd64
  target freebsd arm
  target freebsd arm64
  target freebsd riscv64
  target illumos amd64
  # target ios amd64
  # target ios arm64
  # target js wasm
  target linux 386
  target linux amd64
  target linux arm
  target linux arm64
  target linux loong64
  target linux mips
  target linux mips64
  target linux mips64le
  target linux mipsle
  target linux ppc64
  target linux ppc64le
  target linux riscv64
  target linux s390x
  # target linux sparc64
  target netbsd 386
  target netbsd amd64
  target netbsd arm
  target netbsd arm64
  target openbsd 386
  target openbsd amd64
  target openbsd arm
  target openbsd arm64
  # target openbsd mips64
  target openbsd ppc64
  target openbsd riscv64
  # target plan9 386
  # target plan9 amd64
  # target plan9 arm
  target solaris amd64
  # target wasip1 wasm
  target windows 386 .exe
  target windows amd64 .exe
  target windows arm64 .exe
