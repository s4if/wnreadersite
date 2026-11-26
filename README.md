# wnreadersite
Server to read self-translated using AI webnovel from kaku or syosetu

The library root is the directory holding the project folders, set with
`-rootdir` (default: the folder containing the executable). Every direct
subdirectory of it that contains a pages folder and a `wnreader.toml` is
served as a project.

## Adding a series

Create a subfolder in the library root containing the translated pages
(default `en/`) and a `wnreader.toml` file. A folder is only served while
that file exists, so renaming it away (e.g. to `wnreader.toml.bak`) takes
the series offline. See *Reloading content* below for picking up changes
without a restart; see `wnreader_example.toml` for the available settings.

### Command line options

    -addr 0.0.0.0   address to listen on (0.0.0.0 = all interfaces)
    -port 5500      port to listen on
    -title "…"      library title shown on the home page
                    (default: the library folder's name)
    -dir en         pages subfolder each project must contain
    -ext .txt       fallback translated file extension (default: "ext" in a
                    project's wnreader.toml, else .txt)
    -rootdir /path  library root directory containing the project folders
                    (default: the folder containing the executable)
    -admin 127.0.0.1:5501
                    loopback address of the reload endpoint ("" disables it)

## Reloading content

The library is scanned at startup. To pick up pulled content without a
restart, either trigger re-scans it:

    curl -X POST http://127.0.0.1:5501/reload
    systemctl reload wnreader

The curl hits the reload endpoint: a separate listener bound to loopback
only (`-admin 127.0.0.1:5501`; pass `-admin ""` to disable). It is never
reachable through a reverse proxy, so a local git hook can call it without
sudo even when the server runs as root behind nginx. The `systemctl` path
sends SIGHUP to the process and needs sufficient privileges (root, or a
sudo/polkit rule for your user).

A failed re-scan keeps serving the previous content. Renaming a series'
`wnreader.toml` away and re-scanning takes it offline, same as before.

### Automatic reload on git pull

Install the example hook in the server's clone only — git hooks are
per-clone and never synced, so your laptop and PC are unaffected:

    cp deploy/post-merge.example /path/to/library/.git/hooks/post-merge
    chmod +x /path/to/library/.git/hooks/post-merge

Every successful plain `git pull` then pokes the reload endpoint, making
new series and chapters visible within a second. (`git pull --rebase`
runs a rebase, not a merge, and does not invoke this hook.)

## Building for maximum compatibility

Build with cgo disabled to get a fully static binary that runs on any
glibc-based Linux distribution (e.g. Ubuntu 22.04/24.04), regardless of
the glibc version of the build machine:

    CGO_ENABLED=0 go build -o wnreadersite .

Without this, the default build on a machine with a newer glibc (e.g.
Arch) dynamically links against it and may fail on targets with an
older glibc (`version 'GLIBC_2.xx' not found`).

## Sorting

The home page lists series by their `series` title from `wnreader.toml`,
not by folder name. Titles sort case-insensitively in natural order
(numeric-aware, so `Vol 2` comes before `Vol 10`); series without a
`series` setting fall back to their folder name.
