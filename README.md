# pingtop

> htop-style live dashboard for ping

[![ci](https://github.com/guerrieroriccardo/pingtop/actions/workflows/ci.yml/badge.svg)](https://github.com/guerrieroriccardo/pingtop/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/guerrieroriccardo/pingtop)](https://github.com/guerrieroriccardo/pingtop/releases/latest)
[![aur](https://img.shields.io/aur/version/pingtop-bin)](https://aur.archlinux.org/packages/pingtop-bin)

![pingtop in action](docs/hero.gif)

`pingtop` is a terminal dashboard that continuously pings one or more targets
and shows live RTT, smoothed jitter, packet loss, sent/lost counts, and a
sparkline per row in a sortable table. It takes IPs, hostnames, and CIDR ranges
as arguments. Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea)
and [pro-bing](https://github.com/prometheus-community/pro-bing).

## Why?

- `ping` watches one host at a time.
- `fping` watches many but isn't interactive.
- `mtr` is a route tracer, not a dashboard.

`pingtop` is the gap: a live, multi-host overview you can leave running on a
spare pane while you work.

## Usage

### Watch a few hosts

```sh
pingtop 1.1.1.1 8.8.8.8 example.com
```

![basic dashboard](docs/basic.gif)

Each row updates once per second with the latest RTT, smoothed jitter
(RFC 3550), packet loss, sent/lost count, and a sparkline of recent RTTs scaled
per-target so relative variance is visible. A probe that fails with a network
error (interface down, network unreachable) shows as a red bar on the sparkline
(`×` with `--no-color`), and the target picks up again once the network is back.

### Scan a subnet and prune unreachable hosts

```sh
pingtop -d 5 192.168.1.0/27
```

![cidr scan](docs/scan.gif)

After 5 sends with zero replies, a target is dropped from the table — leaving
only the survivors. Useful for live discovery against a /27 or /28.

### Discovery mode: keep dropped rows visible

If you want to see *which* hosts didn't reply rather than just the survivors,
add `-k`:

```sh
pingtop -k -d 5 192.168.1.0/27
```

![keep dropped](docs/keep-dropped.gif)

Unreachable hosts stay in the table with `100.0%` loss and `5/5` SENT/LOST so
the whole scan result is on screen.

### Sort by column

Press `s` to cycle the sort column forward through the **visible**
columns, `S` to cycle backward, and `r` to flip the direction. After the
last entry the cycle clears, leaving the rows in their original order.

```sh
pingtop 1.1.1.1 8.8.8.8 9.9.9.9 example.com
# inside the UI:
#   s → sorted by TARGET (default direction: descending)
#   r → flips to ascending
#   s → advances to RTT (direction is sticky)
#   s ... → walks through MIN, AVG, MAX, JITTER, LOSS%, then clears
#   S → cycle backward
```

![sort demo](docs/sort.gif)

The active column shows a `↓` or `↑` arrow on its header and the bottom
bar reflects the current key + direction. If the terminal is too narrow
to display the active sort column, the sort silently resets — pick it
again with `s`.

### Filter rows live

Press `/` to start filtering by substring (case-insensitive). The filter
applies as you type — no need to confirm.

```sh
pingtop 10.0.0.0/27 1.1.1.1 8.8.8.8
# inside the UI: press /, type "8.8" → only 8.8.8.8 shown
# press enter to lock in the filter, esc to clear
```

![live filter](docs/filter.gif)

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-i` | `1s` | Interval between pings |
| `-d`, `--drop` | `0` | Drop a target after N sends with no replies (0 = disabled) |
| `-k`, `--keep-dropped` | `false` | Keep dropped rows visible with final stats instead of removing them |
| `-m`, `--max-hosts` | `256` | Hard cap on the number of expanded targets |
| `--no-color` | `false` | Disable color thresholds on RTT/JITTER/LOSS%. `NO_COLOR` env var is also honored |
| `--size` | `24` | ICMP payload size in bytes |

## Keys

| Key | Action |
|-----|--------|
| `↑` / `↓`, `k` / `j` | Scroll the table |
| `s` / `S` | Cycle the sort column forward (`s`) or backward (`S`) among visible columns; past the last entry clears the sort |
| `r` | Reverse the sort direction (no-op when unsorted) |
| `/` | Start a live filter |
| `enter` | Apply the filter (stay filtered, exit edit mode) |
| `esc` | Clear an active filter |
| `q` / `ctrl-c` | Quit |

## Install

### Arch Linux (AUR)

```sh
yay -S pingtop-bin   # prebuilt binary
yay -S pingtop       # build from source
```

### Prebuilt binaries (Linux, macOS — amd64 and arm64)

Grab the right archive from [the latest release](https://github.com/guerrieroriccardo/pingtop/releases/latest),
extract, and put `pingtop` somewhere on your `$PATH`. Example:

```sh
curl -sL https://github.com/guerrieroriccardo/pingtop/releases/latest/download/pingtop_0.13.0_linux_amd64.tar.gz | tar xz
sudo install -m 755 pingtop /usr/local/bin/
```

### Build from source

Requires Go 1.26+.

```sh
git clone https://github.com/guerrieroriccardo/pingtop
cd pingtop
go build -o pingtop .
```

## Privileges

`pingtop` uses unprivileged ICMP where supported. On most modern Linux distros
this Just Works because the kernel allows datagram ICMP for groups listed in
`/proc/sys/net/ipv4/ping_group_range`. If yours is locked down, you have two
options:

```sh
# allow every group system-wide
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"

# or grant raw socket capability to the binary itself
sudo setcap cap_net_raw+ep $(which pingtop)
```

macOS allows unprivileged ICMP for any user; no setup needed.

## Development

Recordings in `docs/` are generated with [vhs](https://github.com/charmbracelet/vhs)
from `.tape` scripts under `docs/recordings/`. Regenerate them all in parallel:

```sh
./docs/recordings/regen.sh
```

Or one at a time:

```sh
vhs docs/recordings/hero.tape
```

Run the tests:

```sh
go test ./...
```

## License

Apache 2.0. See [LICENSE](LICENSE).
