<!--
Copyright 2025-2026 Stanislav Senotrusov

This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.

SPDX-License-Identifier: Apache-2.0 OR MIT
-->

## etcdotica: dotfiles and system config management

*etcdotica* is a lightweight, file-based overlay that synchronizes your system configuration with a Git repository. It treats your repository as a source of truth that casts a shadow onto your filesystem: only the specific paths you track are managed, while everything else remains undisturbed.

This approach provides a predictable and reversible way to manage dotfiles and system artifacts without heavy abstractions or an intermediate configuration layer.

### Architecture overview

#### Per-file overlay model

The tool operates with file-level granularity, treating your repository as a partial map of the system:

- Any file in your source repository is synchronized to its corresponding system path. Files are copied only if content (size or modification time) or permissions have changed.
- Files that exist on the system but are absent from the repository are ignored. `etcdotica` does not own your directories; it only manages the specific artifacts you explicitly track.
- Only files previously managed by `etcdotica` and later deleted from the source are removed from the destination. This is tracked via a local `.etcdotica` state file.

#### Bidirectional workflow

Configuration often happens directly on the system. `etcdotica` supports a circular workflow:

- Push changes from your repository to the system.
- Pull local tweaks from the system back into your repository.

#### Watch mode and safe concurrency

The tool can monitor the source directory for changes and apply them instantly when files are saved. If enabled, it can also collect system-side changes automatically.

It uses advisory file locking (`flock`) to ensure multiple instances can run safely without corrupting files or state. For example, you can run one instance in watch mode while another runs as part of a periodic deployment script.

#### Initial provisioning

For fresh installations, the tool can prioritize repository files over existing system defaults. This reduces machine provisioning to a simple process: clone your repository and run a single command to align the system with your saved state.

#### Section-based file management

For large system files where only specific lines need to be managed, such as entries in `/etc/fstab` or `/etc/hosts`, `etcdotica` supports sections. This allows you to maintain unique configuration snippets without taking ownership of the entire system-generated file. Sections are updated as the source files change, and if a section file is deleted from the source, the corresponding section is automatically removed from the target file.

#### Flexible scope and privilege model

`etcdotica` does not assume any fixed layout or ownership model. You map source directories to destination paths directly and run it with whatever privileges the target requires. This makes the scope entirely opt-in: you can manage a full dotfiles tree or just a handful of files, and apply the same pattern to system configuration without taking ownership of unrelated parts of the filesystem.

It applies the provided or user-default `umask` to ensure files are copied with secure permissions. It can optionally enforce world readability.

It can also optionally ensure that all files within specified source directories (such as `bin/`) have executable bits set before syncing.

### What's in a name?

*etcdotica* fuses the Unix `/etc` directory with the Italian term [*Ecdotica*](https://it.wikipedia.org/wiki/Ecdotica) (*ecdotics* in English). This scholarly discipline is devoted to reconciling divergent manuscript witnesses to produce a "critical edition", a definitive version of a text reconstructed from centuries of manual copying.

The metaphor is deliberate. Curating a modern system is an editorial act. Most configurations are not authored once so much as they are transmitted: a tradition of inherited snippets from strangers and fragments of half-remembered internet threads that somehow survive a decade of migrations, reinstalls, and late-night edits.

*etcdotica* is a small attempt at editorial hygiene for that tradition. It allows you to maintain your configuration in plain text and apply it convergently, without the need for an intermediate software layer that generates and applies your actual configuration.

And despite how the name sounds, there is no distributed consensus here. It simply ensures that the transmission of your `.bashrc` across your personal digital history suffers fewer scribal errors.

### High-level example

Assume your Git repository lives at `~/.dotfiles` and has the following structure:

```text
home/.bashrc
home/.config/systemd/user/etcdotica.service
root/etc/ssh/sshd_config.d/disable-password-authentication.conf # illustrative example
root-only/root/.bashrc
```

You apply the repository files to the system in three passes, each using the appropriate privilege level and scope:

```bash
cd ~/.dotfiles

# 1. User-level files under `home/`
etcdotica \
  -src home \
  -bindir .local/bin \
  -umask 077 \
  -collect

# 2. Root-owned files that must never be readable by unprivileged users
sudo etcdotica \
  -src root-only \
  -umask 077 \
  -collect

# 3. System-wide configuration from `root/`, ensuring files are readable for all users
sudo etcdotica \
  -src root \
  -bindir usr/local/bin \
  -everyone \
  -collect
```

The `-bindir` option is a quality-of-life feature. Any file placed under the specified directory inside the repository is automatically marked executable when synced, so newly created helper scripts are immediately runnable without a manual `chmod`.

To keep user files continuously synchronized, define a user systemd service at `~/.config/systemd/user/etcdotica.service`:

```ini
[Unit]
Description=Etcdotica
ConditionPathExists=%h/.dotfiles

[Service]
Type=exec
WorkingDirectory=%h/.dotfiles
ExecStart=/usr/local/bin/etcdotica \
  -src home \
  -bindir .local/bin \
  -umask 077 \
  -collect \
  -watch

[Install]
WantedBy=default.target
```

Enable and start the service:

```bash
systemctl --user enable --now etcdotica.service
```

With this setup, editing any file in `~/.dotfiles/home` is immediately reflected in your home directory, while still allowing changes made directly on the system to be collected back into the repository.

You can place the service unit file in your `~/.dotfiles` repository at `home/.config/systemd/user/etcdotica.service`, but you must do this before the first manual sync as described above, or simply rerun the sync.

### Installation

To install `etcdotica`, you need the [Go](https://go.dev/) toolchain and the [just](https://github.com/casey/just) command runner configured on your system.

1. Clone the repository:

   ```bash
   git clone https://github.com/senotrusov/etcdotica.git && cd etcdotica
   ```

2. Install the binary:

   ```bash
   just install
   ```

   *(This will compile the binary and perform a system-wide installation to `/usr/local/bin` using `sudo`)*

### Building for development

To compile the executable for local testing without installing it to the system:

```bash
just build
```

*(The resulting binary will be placed in the `./bin/` directory)*

### Usage

To use `etcdotica`, run the binary. You must specify the source directory using the `-src` flag.

You can optionally specify the destination using the `-dest` flag; by default, it uses the user’s home directory, or `/` when running as root.

It automatically excludes `.git` directories and its own state file from synchronization.

#### Options

| Flag | Type | Description |
| :--- | :--- | :--- |
| `-bindir` | `string` | Directory relative to the source directory in which all files will be ensured to have the executable bit set (can be repeated). |
| `-collect` | `bool` | Collect mode: copy newer files from destination back to source. Ignored if `-force` is enabled. |
| `-dst` | `string` | Destination directory (default: user home directory, or / if root). |
| `-everyone` | `bool` | Set group and other permissions to the same permission bits as the owner, then apply the umask to the resulting mode. |
| `-force` | `bool` | Force overwrite even if destination is newer. Overrides `-collect`. |
| `-help` | `bool` | Show help and usage information. |
| `‑log‑format` | `string` | Log format: human, text or json (default "human"). |
| `‑log‑level` | `string` | Log level: debug, info, warn, error (default "info"). |
| `-src` | `string` | Source directory (required). |
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

1. Apply changes from your dotfiles folder to your Home directory:

   ```bash
   etcdotica -src ~/my-dotfiles
   ```

2. Keep the program running to verify changes live, outputting logs in JSON for processing:

   ```bash
   etcdotica -src ~/my-dotfiles -watch -log-format json
   ```

3. Ensure all files in `bin/` and `scripts/` have executable permissions set before syncing:

   ```bash
   etcdotica -src ~/my-dotfiles -bindir .local/bin -bindir scripts
   ```

4. If you edited a config file directly in your home directory and want to save it back to your repo:

   ```bash
   etcdotica -src ~/my-dotfiles -collect
   ```

5. Sync configurations to `/etc` ensuring they are readable by all users:

   ```bash
   sudo etcdotica -src ./etc-files -dst /etc -everyone
   ```

### State & pruning

`etcdotica` creates a hidden file named `.etcdotica` in your source directory. This file tracks every file and section successfully synced.

1. If you delete a file from your source directory, `etcdotica` detects its absence compared to the state file and removes the corresponding file from the destination.
2. If you delete a section file (e.g., `etc/fstab.external-disks-section`) from the source, `etcdotica` will automatically find the target file (`etc/fstab`) and remove only the block belonging to that specific section, leaving the rest of the file untouched.
3. If running as root (e.g., via `sudo`), `etcdotica` attempts to set the ownership of the `.etcdotica` state file to match the owner of the source directory. This prevents the state file from becoming locked to root, ensuring you can still modify your dotfiles repository as a standard user later.

### Managed sections

`etcdotica` supports a special "section" mode that allows you to manage parts of a file without owning the entire file. This is useful for shared system files like `/etc/fstab` or `/etc/hosts`.

#### Naming convention

To use this feature, name your source file using the pattern: `filename.{section-name}-section`.

**Example:**

- Source: `etc/fstab.external-disks-section`
- Target: `etc/fstab`
- Section Name: `external-disks`

#### How it works

The content of the source file is wrapped in `# BEGIN` and `# END` markers and inserted into the target file.

1. If multiple sections exist in the target file, `etcdotica` sorts them alphabetically by their section name.
2. - If a section with the same name already exists, its content is replaced.
   - If no sections exist, the new section is appended to the end of the file.
   - If other sections exist, the new section is inserted in its correct alphabetical position relative to other blocks.
3. All text outside of `# BEGIN` / `# END` blocks is preserved exactly as it is.

#### Safety and validation

To prevent data loss or corruption, `etcdotica` performs safety checks on the destination file:

- If the target file contains a `# BEGIN` or `# END` tag that matches your section name but is missing its counterpart (e.g., a start tag with no end tag), `etcdotica` will stop and refuse to modify the file.
- Malformed tags for sections with *different* names are ignored and treated as raw text to avoid interference with existing file content.

### Symlink behavior at destination

To ensure safety and predictability, `etcdotica` follows specific rules when it encounters an existing symlink at the destination path:

- If the source is a regular file but the destination is a symlink, `etcdotica` will **remove the symlink** and replace it with a standard file. This is a safety feature: it prevents the tool from accidentally overwriting the *contents* of a file located elsewhere on your system that the symlink might be pointing to.
- If a destination path is a symlink that points to an existing directory, `etcdotica` will **preserve the symlink** and sync the source contents into the directory it points to. This allows you to transparently redirect entire configuration folders (such as symlinking `~/.config/app` to a different drive) while still allowing `etcdotica` to manage the files inside.

When the tool identifies an orphaned file at the destination that needs to be removed (because it no longer exists in the source), it uses a safe removal method. If that orphaned file is a symlink, only the symlink pointer itself is deleted; the file or directory it was pointing to remains untouched.

### Concurrency & safety

`etcdotica` is designed for robust operation. It uses advisory file locking (`flock`) on the destination files, section-managed files, and its own `.etcdotica` state file.

This means:

- You can leave one instance running in `-watch` mode and manually trigger another sync without risk of data corruption or race conditions.
- Multiple users or scripts can safely run `etcdotica` against the same destination or source simultaneously.
- While it writes directly to files (to preserve Inodes and hardlinks), the exclusive lock ensures that no other process using standard locking will read a partially written file.

### Direct writes & inode stability

`etcdotica` writes directly to destination files instead of using a "write-to-temp and rename" strategy. This design prioritizes three factors:

- Writing in place keeps the file's inode constant. This preserves existing hardlinks and ensures active system watches (such as `inotify`) remain attached to the file.

- Performing a truly atomic rename requires placing the temporary file on the same filesystem as the destination. Reliably identifying a safe, writable location for these transient files across varying mount points adds significant complexity that falls outside the scope of this tool for now. Choosing to create the temporary file in the same directory as the destination can introduce race conditions, because many directories are automatically scanned for configuration files and not all services reliably ignore temporary files.

- While an atomic write per file sounds appealing, many services load multiple configuration files, so they are still updated one by one, leaving intermediate states observable regardless of per-file atomicity.

- Most services, particularly under `/etc`, traditionally require an explicit reload or restart command to apply configuration changes, so direct writes are generally acceptable because changes are not picked up until the service is reloaded.

- In practice, a transient write failure can usually be resolved by simply re-running the command, which overwrites any partial writes and converges the system to the intended state.

This approach introduces a millisecond-wide window where a service might attempt to read a partially written file if that service does not respect file locks. This is a deliberate choice: in system configuration, a temporary partial read is generally safer and more predictable than the logic conflicts caused by "seeing" extra files in a managed directory.

### Resilience & fault tolerance

If a source directory becomes unavailable during Watch Mode, possibly due to user actions or temporary network unavailability for remote drives, `etcdotica` logs a warning and waits for the source to reappear. Synchronization then resumes automatically, provided the source was successfully located at least once during startup.

### License

`etcdotica` is dual-licensed under the [Apache License, Version 2.0](LICENSE-APACHE) and the [MIT License](LICENSE-MIT). You can choose to use it under the terms of either license. By contributing, you agree to license your contributions under both licenses.
