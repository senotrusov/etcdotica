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

# Retrieve the GPG signing key from the Git configuration if possible
signingkey := `git config --get user.signingkey || true`

# Resolve the version string from Git tags or a fallback VERSION file.
#
# Output Formats:
# - Exact tag: "v1.1.1"
# - Commits since tag: "v1.1.1-4-gabc123" ('g' denotes Git hash)
# - Uncommitted/untracked changes: Suffixes "-dirty"
# - Repository errors: Suffixes "-broken"
# - Fallback: "unknown"
#
# Note: We manually check 'git status' because 'git describe --dirty' may 
# ignore untracked files; this ensures the tag reflects the exact state.
version := `
  set -u # Error on undefined variables

  # Attempt to extract version from Git
  if [ -d .git ]; then
    tag=$(git describe --tags --always --dirty --broken) &&
    status=$(git status --porcelain) || {
      echo "Error: Failed to obtain Git metadata." >&2
      echo "unknown"
      exit
    }

    # Manually append -dirty suffix if uncommitted changes exist
    if [ -n "$tag" ] && [ -n "$status" ]; then
      case "$tag" in
        *-dirty) ;; # Skip if already tagged as dirty
        *) tag="${tag}-dirty" ;; # Append suffix
      esac
    fi
  # Fallback to VERSION file if Git is unavailable
  elif [ -f VERSION ]; then
    read -r tag < VERSION
  fi

  # Final fallback if no version source succeeded
  if [ -z "${tag:-}" ]; then
    tag="unknown"
  fi

  printf "%s\n" "$tag"
`

# Format project files
format:
  mdformat README.md

# Output key project file paths as shell-escaped strings for LLM prompt context
context:
  #!/usr/bin/env bash
  printf "%q\n" go.mod justfile README.md cmd/{{project}}/*.go

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
  set -u # Error on undefined variables
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
  #!/usr/bin/env sh
  set -u # Error on undefined variables

  # Enter the distribution directory or exit if it doesn't exist
  cd dist || { echo "Error: Could not change directory to dist" >&2; exit 1; }

  # Define a helper function to list files in a stable, null-terminated order
  each() {
    find . -maxdepth 1 -type f "$@" -printf '%f\0' | LC_ALL=C sort -z
  }

  # Compress all files not already compressed using ultra zstd compression
  each ! -name '*.zst' | xargs -0 --no-run-if-empty zstd --compress --ultra -20 --rm || {
    echo "Error: Zstd compression failed" >&2
    exit 1
  }

  # Calculate SHA256 checksums for all compressed artifacts
  each ! -name 'SHA256SUMS' | xargs -0 --no-run-if-empty sha256sum > SHA256SUMS || {
    echo "Error: Failed to generate SHA256 checksums" >&2
    exit 1
  }

  # Validate the newly created checksum file against the artifacts
  sha256sum -c SHA256SUMS || { echo "Error: Checksum verification failed" >&2; exit 1; }

  # Ensure a signing key is configured before attempting to sign
  if [ -z "{{signingkey}}" ]; then
    echo "WARNING: No signing key configuration found, the artifacts would not be signed" >&2
    exit
  fi

  # Define a function to create a detached GPG signature and verify it immediately
  sign() {
    gpg --default-key "{{signingkey}}" --armor --detach-sign --output "$1".asc "$1" &&
    gpg --verify "$1".asc "$1" || {
      echo "Error: GPG signing or verification failed for $1" >&2
      exit 1
    }
  }

  # Sign the checksums file to ensure the authenticity of the release
  sign SHA256SUMS

# Create a tarball of the source code
archive-source:
  #!/usr/bin/env sh
  # Ensure the distribution directory exists
  mkdir -p dist

  # Check if the project is a git repository to determine if it's "dirty"
  if [ -d .git ]; then
    # Capture uncommitted changes
    status=$(git status --porcelain) || {
      echo "Error: Failed to obtain git status." >&2
      exit 1
    }
  else
    # Force local file archiving if not in a git repository
    status=sweetheart
  fi

  # Define the target tarball path
  tarfile="dist/{{project}}-{{version}}.tar"

  # Choose archiving method based on repository state
  if [ -n "$status" ]; then
    echo "Creating archive from local files (uncommitted changes detected)..." >&2

    # Create archive manually excluding build artifacts and git metadata
    tar --exclude='./bin' --exclude='./.git' --exclude='./.gitignore' --exclude='./dist' --exclude='./VERSION' --transform='s,^\.,{{project}}-{{version}},' --create --file="$tarfile" . || {
      echo "Error: Failed to create archive from local files." >&2
      exit 1
    }
  else
    echo "Creating archive from git HEAD (repository is clean)..." >&2

    # Create archive using git's internal archiving tool
    git archive --format=tar --prefix="{{project}}-{{version}}/" HEAD > "$tarfile" || {
      echo "Error: Failed to create archive from git HEAD." >&2
      exit 1
    }
  fi

  # Append a version metadata file to the existing tarball
  echo "{{version}}" > dist/VERSION && 
  tar --transform='s,^dist,{{project}}-{{version}},' -rf "$tarfile" dist/VERSION &&
  rm dist/VERSION || {
    echo "Error: Failed to append VERSION file to the archive." >&2
    exit 1
  }

# Cross compile for all platforms
cross-compile:
  #!/usr/bin/env sh
  # https://pkg.go.dev/internal/platform
  # https://go.dev/doc/install/source#environment
  set -u # Error on undefined variables
  target() {
    output="dist/{{project}}-{{version}}-$1-$2${3:-}"
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
