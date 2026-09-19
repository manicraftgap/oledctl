```markdown
# oledctl

This program works as a middleman between brightnessctl and gammastep[cite: 1]. It keeps gammastep's color temperature in sync with brightnessctl's brightness, so it feels like adjusting the same brightness brightnessctl reports[cite: 1]. This is needed on most OLED displays, which lack a backlight and don't expose any way to change the pixel brightness directly[cite: 1].

## Installation

Because `oledctl` wraps existing utilities, it requires **`brightnessctl`** and **`gammastep`** to be installed on your system and accessible in your `$PATH`[cite: 1]. 

### Nix Flakes (NixOS or systems with Nix installed)
You can run it directly from GitHub without cloning:
```bash
nix run github:manicraftgap/oledctl

```

To add it to your NixOS configuration or Home Manager, add it to your flake inputs:

```nix
inputs.oledctl.url = "github:manicraftgap/oledctl";

```

### Arch Linux

First, install the required dependencies and the Go compiler:

```bash
sudo pacman -S brightnessctl gammastep go

```

Then, clone and build the program:

```bash
git clone [https://github.com/manicraftgap/oledctl.git](https://github.com/manicraftgap/oledctl.git)
cd oledctl
go build -o oledctl main.go
sudo mv oledctl /usr/local/bin/

```

### Debian / Ubuntu

First, install the required dependencies and the Go compiler:

```bash
sudo apt update
sudo apt install brightnessctl gammastep golang

```

Then, clone and build the program:

```bash
git clone [https://github.com/manicraftgap/oledctl.git](https://github.com/manicraftgap/oledctl.git)
cd oledctl
go build -o oledctl main.go
sudo mv oledctl /usr/local/bin/

```

### Void Linux

First, install the required dependencies and the Go compiler:

```bash
sudo xbps-install -S brightnessctl gammastep go

```

Then, clone and build the program:

```bash
git clone [https://github.com/manicraftgap/oledctl.git](https://github.com/manicraftgap/oledctl.git)
cd oledctl
go build -o oledctl main.go
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
        gammastep's color temperature to the new brightness[cite: 1]. Any
        argument that isn't an integer is treated as a brightnessctl
        parameter and forwarded as-is (e.g. -q, -p, -d eDP-1)[cite: 1].
    down [STEP] [param...]
        Decrease brightness by STEP percent (default 10), then resync
        gammastep's color temperature to the new brightness[cite: 1]. Same
        argument handling as up[cite: 1].
    set, s [VALUE]
        Set the brightness to a specific VALUE (e.g. 50% or 500). This is 
        passed through to brightnessctl, but oledctl detects the set operation
        and automatically resyncs gammastep to match the new brightness
        afterward[cite: 1].
    status
        Print the current brightness (via brightnessctl -m get)[cite: 1].

OPTIONS
    -h, --help
        Print this help[cite: 1].
    -h, --help brightnessctl
        Print the underlying brightnessctl tool's own help text[cite: 1].

PASSED THROUGH TO BRIGHTNESSCTL
    Every other brightnessctl operation and option — i/info, g/get,
    m/max, -l/--list, -q/--quiet, -p/--pretend, -m/--machine-readable, 
    -n/--min-value, -e/--exponent, -s/--save, -r/--restore, -d/--device, 
    -c/--class, -v/--version — is forwarded to the real brightnessctl 
    verbatim[cite: 1]. Run "oledctl -h brightnessctl" for the full description 
    of each[cite: 1]. 

```

```

```
