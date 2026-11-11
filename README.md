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
the series offline. Restart the app to pick up changes; see
`wnreader_example.toml` for the available settings.

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
