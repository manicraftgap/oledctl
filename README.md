# oledctl (hyprsunset edition)

This program works as a middleman between brightnessctl and hyprsunset. It keeps hyprsunset's gamma filter in sync with brightnessctl's brightness, so it feels like adjusting the same brightness brightnessctl reports. This is needed on most OLED displays, which lack a backlight and don't expose any way to change the pixel brightness directly.

hyprsunset's color temperature filter is kept permanently disabled (`identity`) — only its gamma filter is used, so brightness changes never shift color temperature.

Unlike the gammastep-based version of oledctl, this version never starts or stops a background process itself. hyprsunset is expected to already be running as your own persistent daemon, and oledctl just sends it live updates over `hyprctl`'s IPC. That also means there's nothing to hand off between processes on each brightness change, so there's no gap where the display could flash to a default gamma table.

## Requirements

- **Hyprland** — hyprsunset's IPC is exposed through `hyprctl`, so this version only works under Hyprland.
- **hyprsunset** must be running as a persistent daemon before oledctl is used. It is not started by oledctl. Add one of the following to your Hyprland setup:
  ```
  exec-once = hyprsunset
  ```
  in `hyprland.conf`, or enable it as a systemd user service:
  ```bash
  systemctl --user enable --now hyprsunset.service
  ```
- **`brightnessctl`** and **`hyprctl`** (the latter ships with Hyprland) must be installed and accessible in your `$PATH`.

## Installation

### Nix Flakes (NixOS or systems with Nix installed)
You can run it directly from GitHub without cloning:
```bash
nix run github:manicraftgap/oledctl-hyprsunset
```

To add it to your NixOS configuration or Home Manager, add it to your flake inputs:
```nix
inputs.oledctl-hyprsunset.url = "github:manicraftgap/oledctl-hyprsunset";
```

### Arch Linux

First, install the required dependencies and the Go compiler:
```bash
sudo pacman -S brightnessctl hyprsunset go
```

Then, clone and build the program:
```bash
git clone https://github.com/manicraftgap/oledctl-hyprsunset.git
cd oledctl-hyprsunset
go build -o oledctl ./cmd/oledctl
sudo mv oledctl /usr/local/bin/
```

### Debian / Ubuntu

First, install the required dependencies and the Go compiler (hyprsunset is not packaged for Debian/Ubuntu — build it from source or use its AppImage; see the [hyprsunset wiki](https://wiki.hypr.land/Hypr-Ecosystem/hyprsunset/)):
```bash
sudo apt update
sudo apt install brightnessctl golang
```

Then, clone and build the program:
```bash
git clone https://github.com/manicraftgap/oledctl-hyprsunset.git
cd oledctl-hyprsunset
go build -o oledctl ./cmd/oledctl
sudo mv oledctl /usr/local/bin/
```

### Void Linux

First, install the required dependencies and the Go compiler:
```bash
sudo xbps-install -S brightnessctl hyprsunset go
```

Then, clone and build the program:
```bash
git clone https://github.com/manicraftgap/oledctl-hyprsunset.git
cd oledctl-hyprsunset
go build -o oledctl ./cmd/oledctl
sudo mv oledctl /usr/local/bin/
```

## Permissions

Modifying brightness requires the underlying `brightnessctl` command to have write permissions for device files or systemd support. This is typically accomplished by:

1. Installing relevant udev rules to add permissions to backlight class devices for users in the `video` group.
2. Installing `brightnessctl` as a suid binary.
3. Using the `systemd-logind` API.

## Usage

```text
NAME
    oledctl - sync display brightness and OLED-safe color temperature

SYNOPSIS
    oledctl [operation] [value...]
    oledctl [brightnessctl options] [operation] [value...]
    oledctl -h | --help [brightnessctl]

OLEDCTL OPERATIONS
    up [STEP] [param...]
        Increase brightness by STEP percent (default 10), then resync
        hyprsunset's gamma filter to the new brightness. Any argument
        that isn't an integer is treated as a brightnessctl parameter
        and forwarded as-is (e.g. -q, -p, -d eDP-1).
    down [STEP] [param...]
        Decrease brightness by STEP percent (default 10), then resync
        hyprsunset's gamma filter to the new brightness. Same argument
        handling as up.
    set, s [VALUE]
        Set the brightness to a specific VALUE (e.g. 50% or 500). This is
        passed through to brightnessctl, but oledctl detects the set
        operation and automatically resyncs hyprsunset to match the new
        brightness afterward.
    status
        Print the current brightness (via brightnessctl -m get).

OPTIONS
    -h, --help
        Print this help.
    -h, --help brightnessctl
        Print the underlying brightnessctl tool's own help text.

PASSED THROUGH TO BRIGHTNESSCTL
    Every other brightnessctl operation and option — i/info, g/get,
    m/max, -l/--list, -q/--quiet, -p/--pretend, -m/--machine-readable,
    -n/--min-value, -e/--exponent, -s/--save, -r/--restore, -d/--device,
    -c/--class, -v/--version — is forwarded to the real brightnessctl
    verbatim. Run "oledctl -h brightnessctl" for the full description
    of each.
```
