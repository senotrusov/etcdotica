## etcdotica

**etcdotica** is a lightweight, zero-dependency tool written in Go to synchronize files from your current directory to your home directory. It is designed to make managing dotfiles simple, idempotent, and consistent.

### 🚀 Features

- **One-Way Synchronization:** Mirrors the current working directory to the current user's home directory (or `/` if running as root).
- **Smart Updates:** Only copies files if the content (size/modification time) or permissions have changed.
- **Pruning:** Automatically removes files from the destination if they are deleted from the source (tracked via a local `.etcdotica` state file).
- **Permission Handling:** Applies the system `umask` to ensures files are copied with correct and secure permissions.
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

### 💡 Usage

To use `etcdotica`, navigate to your dotfiles directory (the source) and run the binary. The program treats the current working directory as the source of truth.

#### Options

| Flag | Type | Description |
| :--- | :--- | :--- |
| `-watch` | `bool` | Enables watch mode. The program will run continuously, scanning for and syncing changes. |

#### Examples

**1. Standard Sync**
Navigate to your dotfiles folder and apply changes once:

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

**3. State Tracking**
`etcdotica` creates a hidden file named `.etcdotica` in your source directory. This file tracks which files have been installed, allowing the tool to clean up (delete) files from your home directory if you remove them from your dotfiles repo.

### License

`etcdotica` is licensed under the [Apache License 2.0](LICENSE).
