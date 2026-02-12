<!--
  Copyright 2025-2026 Stanislav Senotrusov

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->

## etcdotica

**etcdotica** is a lightweight, zero-dependency tool written in Go to synchronize files from a source directory to a destination directory (defaulting to your home directory). It is designed to make managing dotfiles simple, idempotent, and consistent.

### 🚀 Features

- **One-Way Synchronization:** Mirrors the source directory to the destination directory.
- **Bi-directional Collection:** Optionally copy newer files from the destination back to the source ("Collect Mode").
- **Smart Updates:** Only copies files if the content (size/modification time) or permissions have changed.
- **Managed Sections:** Merge content into existing files instead of overwriting them, using sections.
- **Automatic Pruning:** Removes files from the destination if they are deleted from the source (tracked via a local `.etcdotica` state file).
- **Section Rollback:** If a section file is deleted from the source, the corresponding section is automatically removed from the target file.
- **Safe Concurrency:** Uses advisory file locking (`flock`) to ensure that multiple instances can run safely without corrupting files or the state.
- **Permission Handling:** Applies the system (or provided) `umask` to ensure files are copied with correct and secure permissions. Can optionally enforce world-readability.
- **Executable Enforcement:** Optionally scans specified directories (like `bin/`) and ensures all files within them have executable bits set before syncing, respecting system `umask`.
- **Symlink Resolution:** Follows and resolves symlinks in the source directory before copying the actual content to the destination.
- **Watch Mode:** Optionally watches the source directory for changes and syncs automatically. Handles transient unavailability of the source (e.g., if a mount point is temporarily disconnected).
- **Clean:** Automatically ignores `.git` directories and its own state file.

### 📦 Installation

Since `etcdotica` is written in Go, you can easily install it if you have the Go toolchain configured:

1. **Clone the repository:**
   ```bash
   git clone https://github.com/senotrusov/etcdotica.git
   cd etcdotica
   ```
1. **Install the binary:**
   ```bash
   go install
   ```
   *(The `etcdotica` binary will be placed in your `$GOPATH/bin` or `$GOBIN`)*

### ⚙️ Building for Development

To compile the executable in the current directory for local testing or development:

```bash
go build
```

### 🔨 Static Compilation (Portability)

If you want to create a single, portable binary that runs on any Linux distribution without requiring the Go toolchain or any shared C libraries (like `glibc`), use the following static build command:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o etcdotica
```

#### Why use this?

- **Zero Dependencies:** The resulting binary contains everything it needs to run.
- **Portability:** Works across different Linux distros (e.g., from Ubuntu to Alpine/musl).
- **Smaller Size:** The `-s -w` flags strip debug information and the symbol table to reduce the file size.

#### Cross-Compilation Recipe

To build a static binary for a different architecture (e.g., building for a Raspberry Pi or a remote server from your local machine):

| Target | Command |
| :--- | :--- |
| **Linux (64-bit)** | `CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o etcdotica-linux-amd64` |
| **Linux (ARM64)** | `CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o etcdotica-linux-arm64` |
| **macOS (Intel)** | `CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o etcdotica-darwin-amd64` |
| **macOS (M1/M2)** | `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o etcdotica-darwin-arm64` |

### 🔍 Verifying the Build (Reproducibility)

`etcdotica` supports **reproducible builds**. This means that two different people compiling the same version of the source code on different machines will produce a binary with the exact same internal **BuildID**.

The `-trimpath` flag used in the build recipes above is essential for this; it strips local file system paths (e.g., `/home/user/etcdotica`) from the binary, ensuring the fingerprint is generated solely from the source code itself.

To extract the unique fingerprint of your binary, use the Go toolchain:

```bash
go tool buildid etcdotica
```

If your output matches the BuildID of an official release or a build from a colleague, you have verified that the binary was built from the exact same source code without any modifications or environment-specific interference.

### 💡 Usage

To use `etcdotica`, run the binary. You must specify the source directory using the `-src` flag. By default, the program treats your home directory as the destination.

#### Options

| Flag | Type | Description |
| :--- | :--- | :--- |
| `-src` | `string` | **Required.** Source directory containing your files. |
| `-dst` | `string` | Destination directory (default: user home directory, or `/` if root). |
| `-watch` | `bool` | Enables watch mode. The program will run continuously, scanning for and syncing changes. |
| `-collect` | `bool` | Collect mode: Copy newer files from the destination back to the source. |
| `-force` | `bool` | Force overwrite even if the destination file is newer than the source file. |
| `-bindir` | `string` | Specifies a directory relative to the source directory where files must be executable. Can be repeated. |
| `-umask` | `string` | Set process umask (octal, e.g. `022`). |
| `-everyone` | `bool` | Set group and other permissions to the same permission bits as the owner, then apply the umask to the resulting mode. |
| `-log-format` | `string` | Log output format: `human` (default), `text`, or `json`. |
| `-log-level` | `string` | Log level: `debug`, `info`, `warn`, `error` (default: `info`). |

#### Environment Variables

Etcdotica also respects the following environment variables, which can be useful for containerized environments or scripts:

| Variable | Description |
| :--- | :--- |
| `EDTC_LOG_LEVEL` | Sets the default log level (`debug`, `info`, `warn`, `error`). Overridden by `-log-level`. |
| `EDTC_FORCE` | If set to `1` or `true`, enables force mode (equivalent to `-force`). |
| `EDTC_COLLECT` | If set to `1` or `true`, enables collect mode (equivalent to `-collect`). |

#### Examples

**1. Standard Sync**
Apply changes from your dotfiles folder to your Home directory:

```bash
etcdotica -src ~/my-dotfiles
```

**2. Watch Mode with JSON Logging**
Keep the program running to verify changes live, outputting logs in JSON for processing:

```bash
etcdotica -src ~/my-dotfiles -watch -log-format json
```

**3. Executable Directories**
Ensure all files in `bin/` and `scripts/` have executable permissions set before syncing:

```bash
etcdotica -src ~/my-dotfiles -bindir .local/bin -bindir scripts
```

**4. Collect Mode**
If you edited a config file directly in your home directory and want to save it back to your repo:

```bash
etcdotica -src ~/my-dotfiles -collect
```

**5. System Configuration (Root)**
Sync configurations to `/etc` ensuring they are readable by all users:

```bash
sudo etcdotica -src ./etc-files -dst /etc -everyone
```

### 🔄 State & Pruning

`etcdotica` creates a hidden file named `.etcdotica` in your source directory. This file tracks every file and section successfully synced.

1. **File Removal:** If you delete a file from your source directory, `etcdotica` detects its absence compared to the state file and removes the corresponding file from the destination.
1. **Section Removal:** If you delete a section file (e.g., `etc/fstab.mounts-section`) from the source, `etcdotica` will automatically find the target file (`etc/fstab`) and remove only the block belonging to that specific section, leaving the rest of the file untouched.
1. **Root Ownership Fix:** If running as root (e.g., via `sudo`), `etcdotica` attempts to set the ownership of the `.etcdotica` state file to match the owner of the source directory. This prevents the state file from becoming locked to root, ensuring you can still modify your dotfiles repository as a standard user later.

### 🧩 Managed Sections

`etcdotica` supports a special "section" mode that allows you to manage parts of a file without owning the entire file. This is useful for shared system files like `/etc/fstab` or `/etc/hosts`.

#### Naming Convention

To use this feature, name your source file using the pattern: `filename.{section-name}-section`.

**Example:**

- Source: `etc/fstab.external-disks-section`
- Target: `etc/fstab`
- Section Name: `external-disks`

#### How it works

The content of the source file is wrapped in `# BEGIN` and `# END` markers and inserted into the target file.

1. **Alphabetical Sorting:** If multiple sections exist in the target file, `etcdotica` sorts them alphabetically by their section name.
1. **Insertion Rules:**
   - If a section with the same name already exists, its content is replaced.
   - If no sections exist, the new section is appended to the end of the file.
   - If other sections exist, the new section is inserted in its correct alphabetical position relative to other blocks.
1. **Preservation:** All text outside of `# BEGIN` / `# END` blocks is preserved exactly as it is.

#### Safety and Validation

To prevent data loss or corruption, `etcdotica` performs safety checks on the destination file:

- **Orphaned Tags:** If the target file contains a `# BEGIN` or `# END` tag that matches your section name but is missing its counterpart (e.g., a start tag with no end tag), **`etcdotica` will stop and refuse to modify the file**.
- **Unrelated Tags:** Malformed tags for sections with *different* names are ignored and treated as raw text to avoid interference with existing file content.

### 🔗 Symlink Behavior at Destination

To ensure safety and predictability, `etcdotica` follows specific rules when it encounters an existing symlink at the destination path:

- **For Files:** If the source is a regular file but the destination is a symlink, `etcdotica` will **remove the symlink** and replace it with a standard file. This is a safety feature: it prevents the tool from accidentally overwriting the *contents* of a file located elsewhere on your system that the symlink might be pointing to.
- **For Directories:** If a destination path is a symlink that points to an existing directory, `etcdotica` will **preserve the symlink** and sync the source contents into the directory it points to. This allows you to transparently redirect entire configuration folders (such as symlinking `~/.config/app` to a different drive) while still allowing `etcdotica` to manage the files inside.

**Pruning Safety:** When the tool identifies an orphaned file at the destination that needs to be removed (because it no longer exists in the source), it uses a safe removal method. If that orphaned file is a symlink, only the symlink pointer itself is deleted; the file or directory it was pointing to remains untouched.

### 🔒 Concurrency & Safety

`etcdotica` is designed for robust operation. It uses **advisory file locking** (`flock`) on the destination files, section-managed files, and its own `.etcdotica` state file.

This means:

- **Watch Mode + Manual Sync:** You can leave one instance running in `-watch` mode and manually trigger another sync without risk of data corruption or race conditions.
- **Multiple Instances:** Multiple users or scripts can safely run `etcdotica` against the same destination or source simultaneously.
- **Lock-guarded writes:** While it writes directly to files (to preserve Inodes and hardlinks), the exclusive lock ensures that no other process using standard locking will read a partially written file.

### ⚠️ Direct Writes & Inode Stability

`etcdotica` writes directly to destination files instead of using a "write-to-temp and rename" strategy. This design prioritizes three factors:

- **Avoiding Race Conditions:** Many directories are automatically scanned for configuration files. While some services use filters to ignore temporary files, this behavior is not universal. Direct writes ensure no auxiliary files are ever created, eliminating the risk of a service "double-loading" unintended configuration.
- **Stable Inodes:** Writing in-place keeps the file's Inode constant. This preserves existing hardlinks and ensures active system watches (like `inotify`) remain attached to the file.
- **Architectural Simplicity:** Performing a truly atomic rename requires placing the temporary file on the same filesystem as the destination. Reliably identifying a safe, writeable location for these transients across varying mount points adds significant complexity that falls outside the scope of this tool.

**The Trade-off:** This approach introduces a millisecond-wide window where a service might attempt to read a **partially written** file if that service does not respect file locks. This is a deliberate choice: in system configuration, a temporary partial read is generally safer and more predictable than the logic conflicts caused by "seeing" extra files in a managed directory.

### Resilience & Fault Tolerance

- **Connection recovery:** If a source directory (such as a network drive) becomes unavailable during Watch Mode, `etcdotica` logs a warning and waits for the source to reappear, then automatically resumes synchronization, provided the source was successfully located at least once during startup.

### License

`etcdotica` is licensed under the [Apache License 2.0](LICENSE).
