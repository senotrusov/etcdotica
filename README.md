## etcdotica

**etcdotica** is a lightweight, zero-dependency tool written in Go to synchronize files from a source directory (defaulting to your current directory) to a destination directory (defaulting to your home directory). It is designed to make managing dotfiles simple, idempotent, and consistent.

### 🚀 Features

- **One-Way Synchronization:** Mirrors the source directory to the destination directory. Defaults to syncing the current working directory to the current user's home directory (or `/` if running as root).
- **Smart Updates:** Only copies files if the content (size/modification time) or permissions have changed.
- **Pruning:** Automatically removes files from the destination if they are deleted from the source (tracked via a local `.etcdotica` state file).
- **Permission Handling:** Applies the system (or provided) `umask` to ensure files are copied with correct and secure permissions. Can optionally enforce world-readability.
- **Executable Enforcement:** Optionally scans specified directories (like `bin/`) and ensures all files within them have executable bits set before syncing, respecting system `umask`.
- **Symlink Resolution:** Follows and resolves symlinks in the source directory before copying the actual content to the destination.
- **Watch Mode:** Optionally watches the source directory for changes and syncs automatically.
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

To use `etcdotica`, run the binary. By default, the program treats the current working directory as the source and your home directory as the destination. You can override these paths using flags.

#### Options

| Flag | Type | Description |
| :--- | :--- | :--- |
| `-src` | `string` | Source directory (default: current working directory). |
| `-dst` | `string` | Destination directory (default: user home directory, or `/` if root). |
| `-watch` | `bool` | Enables watch mode. The program will run continuously, scanning for and syncing changes. |
| `-bindir` | `string` | Specifies a directory relative to the source directory where files must be executable. Can be repeated. |
| `-umask` | `string` | Set process umask (octal, e.g. `022`). |
| `-everyone` | `bool` | Set permissions to world-readable (and executable if user-executable), disregarding source group/other bits. |

#### Examples

**1. Standard Sync**
Navigate to your dotfiles folder and apply changes once (syncs PWD to Home):

```bash
cd ~/my-dotfiles
etcdotica
```

**2. Watch Mode**
Keep the program running to verify changes live while editing configurations:

```bash
cd ~/my-dotfiles
etcdotica -watch
```

**3. Executable Directories**
Ensure all files in `bin/` and `scripts/` have executable permissions set before syncing:

```bash
cd ~/my-dotfiles
etcdotica -bindir .local/bin -bindir scripts
```

**4. Custom Source and Destination**
Sync a specific configuration folder to a backup location:

```bash
etcdotica -src ./configs -dst /tmp/configs-backup
```

**5. System Configuration (Root)**
Sync configurations to `/etc` ensuring they are readable by all users:

```bash
sudo etcdotica -src ./etc-files -dst /etc -everyone
```

**6. State Tracking**
`etcdotica` creates a hidden file named `.etcdotica` in your source directory. This file tracks which files have been installed, allowing the tool to clean up (delete) files from your destination directory if you remove them from your source.

### 🔗 Symlink Behavior at Destination

To ensure safety and predictability, `etcdotica` follows specific rules when it encounters an existing symlink at the destination path:

* **For Files:** If the source is a regular file but the destination is a symlink, `etcdotica` will **remove the symlink** and replace it with a standard file. This is a safety feature: it prevents the tool from accidentally overwriting the *contents* of a file located elsewhere on your system that the symlink might be pointing to.
* **For Directories:** If a destination path is a symlink that points to an existing directory, `etcdotica` will **preserve the symlink** and sync the source contents into the directory it points to. This allows you to transparently redirect entire configuration folders (such as symlinking `~/.config/app` to a different drive) while still allowing `etcdotica` to manage the files inside.

**Pruning Safety:** When the tool identifies an orphaned file at the destination that needs to be removed (because it no longer exists in the source), it uses a safe removal method. If that orphaned file is a symlink, only the symlink pointer itself is deleted; the file or directory it was pointing to remains untouched.

### ⚠️ Direct Writes & Inode Stability

`etcdotica` writes directly to destination files instead of using a "write-to-temp and rename" strategy. This design prioritizes three factors:

* **Avoiding Race Conditions:** Many directories are automatically scanned for configuration files. While some services use filters to ignore temporary files, this behavior is not universal. Direct writes ensure no auxiliary files are ever created, eliminating the risk of a service "double-loading" unintended configuration.
* **Stable Inodes:** Writing in-place keeps the file's Inode constant. This preserves existing hardlinks and ensures active system watches (like `inotify`) remain attached to the file.
* **Architectural Simplicity:** Performing a truly atomic rename requires placing the temporary file on the same filesystem as the destination. Reliably identifying a safe, writeable location for these transients across varying mount points adds significant complexity that falls outside the scope of this tool.

**The Trade-off:** This approach introduces a millisecond-wide window where a service might attempt to read a **partially written** file. This is a deliberate choice: in system configuration, a temporary partial read is generally safer and more predictable than the logic conflicts caused by "seeing" extra files in a managed directory.

### License

`etcdotica` is licensed under the [Apache License 2.0](LICENSE).
