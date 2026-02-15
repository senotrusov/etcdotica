<!--
Copyright 2025-2026 Stanislav Senotrusov

This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.

SPDX-License-Identifier: Apache-2.0 OR MIT
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

To install `etcdotica`, you need the [Go](https://go.dev/) toolchain and the [just](https://github.com/casey/just) command runner configured on your system.

1. **Clone the repository:**
   ```bash
   git clone https://github.com/senotrusov/etcdotica.git && cd etcdotica
   ```
1. **Install the binary:**
   ```bash
   just install
   ```
   *(This will compile the binary and perform a system-wide installation to `/usr/local/bin` using `sudo`)*

### ⚙️ Building for development

To compile the executable for local testing without installing it to the system:

```bash
just build
```

*(The resulting binary will be placed in the `./bin/` directory)*

### 💡 Usage

To use `etcdotica`, run the binary. You must specify the source directory using the `-src` flag. By default, the program treats your home directory as the destination.

#### Options

| Flag | Type | Description |
| :--- | :--- | :--- |
| `-bindir` | `string` | Directory relative to the source directory in which all files will be ensured to have the executable bit set (can be repeated). |
| `-collect` | `bool` | Collect mode: copy newer files from destination back to source. Ignored if `-force` is enabled. |
| `-dst` | `string` | Destination directory (default: user home directory, or / if root). |
| `-everyone` | `bool` | Set group and other permissions to the same permission bits as the owner, then apply the umask to the resulting mode. |
| `-force` | `bool` | Force overwrite even if destination is newer. Overrides `-collect`. |
| `-help` | `bool` | Show help and usage information. |
| `-log-format` | `string` | Log format: human, text or json (default "human"). |
| `-log-level` | `string` | Log level: debug, info, warn, error (default "info"). |
| `-src` | `string` | Source directory **(required)**. |
| `-umask` | `string` | Set process umask (octal, e.g. 077). |
| `-version` | `bool` | Print version information and exit. |
| `-watch` | `bool` | Watch mode: scan continuously for changes. |

#### Environment variables

Etcdotica also respects the following environment variables, which can be useful for containerized environments or scripts:

| Variable | Description |
| :--- | :--- |
| `EDTC_LOG_LEVEL` | Sets the default log level (`debug`, `info`, `warn`, `error`). Overridden by `-log-level`. |
| `EDTC_FORCE` | If set to `1` or `true`, enables force mode (equivalent to `-force`). Overrides collect mode. |
| `EDTC_COLLECT` | If set to `1` or `true`, enables collect mode (equivalent to `-collect`). Ignored if force mode is enabled. |

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

### 🔄 State & pruning

`etcdotica` creates a hidden file named `.etcdotica` in your source directory. This file tracks every file and section successfully synced.

1. **File Removal:** If you delete a file from your source directory, `etcdotica` detects its absence compared to the state file and removes the corresponding file from the destination.
1. **Section Removal:** If you delete a section file (e.g., `etc/fstab.mounts-section`) from the source, `etcdotica` will automatically find the target file (`etc/fstab`) and remove only the block belonging to that specific section, leaving the rest of the file untouched.
1. **Root Ownership Fix:** If running as root (e.g., via `sudo`), `etcdotica` attempts to set the ownership of the `.etcdotica` state file to match the owner of the source directory. This prevents the state file from becoming locked to root, ensuring you can still modify your dotfiles repository as a standard user later.

### 🧩 Managed sections

`etcdotica` supports a special "section" mode that allows you to manage parts of a file without owning the entire file. This is useful for shared system files like `/etc/fstab` or `/etc/hosts`.

#### Naming convention

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

#### Safety and validation

To prevent data loss or corruption, `etcdotica` performs safety checks on the destination file:

- **Orphaned Tags:** If the target file contains a `# BEGIN` or `# END` tag that matches your section name but is missing its counterpart (e.g., a start tag with no end tag), **`etcdotica` will stop and refuse to modify the file**.
- **Unrelated Tags:** Malformed tags for sections with *different* names are ignored and treated as raw text to avoid interference with existing file content.

### 🔗 Symlink behavior at destination

To ensure safety and predictability, `etcdotica` follows specific rules when it encounters an existing symlink at the destination path:

- **For Files:** If the source is a regular file but the destination is a symlink, `etcdotica` will **remove the symlink** and replace it with a standard file. This is a safety feature: it prevents the tool from accidentally overwriting the *contents* of a file located elsewhere on your system that the symlink might be pointing to.
- **For Directories:** If a destination path is a symlink that points to an existing directory, `etcdotica` will **preserve the symlink** and sync the source contents into the directory it points to. This allows you to transparently redirect entire configuration folders (such as symlinking `~/.config/app` to a different drive) while still allowing `etcdotica` to manage the files inside.

**Pruning Safety:** When the tool identifies an orphaned file at the destination that needs to be removed (because it no longer exists in the source), it uses a safe removal method. If that orphaned file is a symlink, only the symlink pointer itself is deleted; the file or directory it was pointing to remains untouched.

### 🔒 Concurrency & safety

`etcdotica` is designed for robust operation. It uses **advisory file locking** (`flock`) on the destination files, section-managed files, and its own `.etcdotica` state file.

This means:

- **Watch Mode + Manual Sync:** You can leave one instance running in `-watch` mode and manually trigger another sync without risk of data corruption or race conditions.
- **Multiple Instances:** Multiple users or scripts can safely run `etcdotica` against the same destination or source simultaneously.
- **Lock-guarded writes:** While it writes directly to files (to preserve Inodes and hardlinks), the exclusive lock ensures that no other process using standard locking will read a partially written file.

### ⚠️ Direct writes & inode stability

`etcdotica` writes directly to destination files instead of using a “write-to-temp and rename” strategy. This design prioritizes three factors:

- **Stable inodes:** Writing in place keeps the file’s inode constant. This preserves existing hardlinks and ensures active system watches (such as `inotify`) remain attached to the file.

- **Architectural simplicity:** Performing a truly atomic rename requires placing the temporary file on the same filesystem as the destination. Reliably identifying a safe, writable location for these transient files across varying mount points adds significant complexity that falls outside the scope of this tool for now. Choosing to create the temporary file in the same directory as the destination can introduce race conditions, because many directories are automatically scanned for configuration files and not all services reliably ignore temporary files.

- **Limits of atomic writes across multiple files:** While an atomic write per file sounds appealing, many services load multiple configuration files, so they are still updated one by one, leaving intermediate states observable regardless of per-file atomicity.

- **Operational reality:** Most services, particularly under `/etc`, traditionally require an explicit reload or restart command to apply configuration changes, so direct writes are generally acceptable because changes are not picked up until the service is reloaded.

- **Recovery from transient write failures:** In practice, a transient write failure can usually be resolved by simply re-running the command, which overwrites any partial writes and converges the system to the intended state.

**The trade-off:** This approach introduces a millisecond-wide window where a service might attempt to read a partially written file if that service does not respect file locks. This is a deliberate choice: in system configuration, a temporary partial read is generally safer and more predictable than the logic conflicts caused by “seeing” extra files in a managed directory.

### Resilience & fault tolerance

- **Source recovery:** If a source directory becomes unavailable during Watch Mode, possibly due to user actions or temporary network unavailability for remote drives, `etcdotica` logs a warning and waits for the source to reappear. Synchronization then resumes automatically, provided the source was successfully located at least once during startup.

### License

`etcdotica` is dual-licensed under the [Apache License, Version 2.0](LICENSE-APACHE) and the [MIT License](LICENSE-MIT). You can choose to use it under the terms of either license. By contributing, you agree to license your contributions under both licenses.
