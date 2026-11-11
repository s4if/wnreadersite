# wnreadersite
Server to read self-translated using AI webnovel from kaku or syosetu

## Adding a series

Create a subfolder in the library root containing the translated pages
(default `en/`) and a `wnreader.toml` file. A folder is only served while
that file exists, so renaming it away (e.g. to `wnreader.toml.bak`) takes
the series offline. Restart the app to pick up changes; see
`wnreader_example.toml` for the available settings.

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
