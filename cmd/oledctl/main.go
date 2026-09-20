package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func findRealBrightnessctl() (string, error) {
	// 1. If Nix specified an explicit path, use it directly
	if envPath := os.Getenv("REAL_BRIGHTNESSCTL"); envPath != "" {
		return envPath, nil
	}

	// Look through PATH, skipping any binary that resolves to this current executable
	selfPath, _ := os.Executable()
	pathEnv := os.Getenv("PATH")
	paths := strings.Split(pathEnv, string(os.PathListSeparator))

	for _, p := range paths {
		candidate := p + "/brightnessctl"
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if candidate != selfPath {
				return candidate, nil
			}
		}
	}

	return "/usr/bin/brightnessctl", nil
}

var (
	currentBrightness int // percentage, 0-100
	newBrightness     int
)

// commandTimeout bounds how long any external command (brightnessctl or
// hyprctl) is allowed to run. Unlike the gammastep version of oledctl,
// this applies uniformly — there's no long-lived background process to
// special-case here, since hyprsunset is its own persistent daemon that
// oledctl never starts or stops. Every call oledctl makes is a quick,
// synchronous IPC round trip that's expected to return almost instantly.
const commandTimeout = 5 * time.Second

func runCommand(name string, args ...string) (string, error) {
	// If oledctl calls brightnessctl internally, route to the real binary directly
	if name == "brightnessctl" {
		if realPath, err := findRealBrightnessctl(); err == nil {
			name = realPath
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out after %s", name, commandTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w (stderr: %s)", name, err, stderr.String())
	}
	return stdout.String(), nil
}

// clampPercent constrains a brightness percentage to hyprsunset's gamma
// range. hyprsunset's default max-gamma is 100 (its absolute ceiling is
// 200, configurable via hyprsunset.conf), so 0-100 is the range that's
// guaranteed to work without the user needing to raise max-gamma.
func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// syncHyprsunset applies the given brightness percentage via hyprsunset's
// gamma filter, keeping the color temperature filter permanently
// disabled (`identity`) so only perceived brightness changes — mirroring
// the fixed-6500K behavior of the gammastep-based version of oledctl.
//
// Unlike gammastep, hyprsunset is not something oledctl starts or stops.
// It's expected to already be running as a long-lived daemon (typically
// via `exec-once = hyprsunset` in hyprland.conf, or its systemd user
// service). oledctl just sends it a live update over hyprctl's IPC —
// there's no process handoff, no gamma-control object to hand off
// between processes, and so no gap where the compositor could fall back
// to a default gamma table. That handoff gap — and the resulting blink
// on every brightness change — is exactly what made the gammastep-based
// version tricky to get right; this version doesn't have the problem to
// begin with, since the daemon holding the gamma-control object never
// changes, only the values it's told to apply.
//
// If hyprsunset isn't running, the `hyprctl hyprsunset ...` calls below
// fail fast (hyprctl returns an error rather than hanging), and that
// error is surfaced to the user rather than silently swallowed.
func syncHyprsunset(percent int) error {
	if _, err := runCommand("hyprctl", "hyprsunset", "identity"); err != nil {
		return fmt.Errorf("disabling hyprsunset color temperature filter: %w (is hyprsunset running?)", err)
	}

	if _, err := runCommand("hyprctl", "hyprsunset", "gamma", strconv.Itoa(clampPercent(percent))); err != nil {
		return fmt.Errorf("setting hyprsunset gamma: %w (is hyprsunset running?)", err)
	}

	return nil
}

// printStatus prints brightnessctl's own status line verbatim via
// `brightnessctl -m get`.
func printStatus() error {
	out, err := runCommand("brightnessctl", "-m", "get")
	if err != nil {
		return fmt.Errorf("reading brightness: %w", err)
	}
	fmt.Print(out)
	return nil
}

// getBrightness reads the current brightness percentage fresh from
// brightnessctl into currentBrightness. Used internally
// wherever we need a 0-100 number to drive hyprsunset's gamma value
// (e.g. syncing after a brightness change).
func getBrightness() error {
	out, err := runCommand("brightnessctl", "-m")
	if err != nil {
		return fmt.Errorf("reading brightness: %w", err)
	}

	fields := strings.Split(strings.TrimSpace(out), ",")
	if len(fields) < 4 {
		return fmt.Errorf("unexpected brightnessctl output: %q", out)
	}

	pctStr := strings.TrimSuffix(fields[3], "%")
	pct, err := strconv.Atoi(pctStr)
	if err != nil {
		return fmt.Errorf("parsing brightness percentage %q: %w", fields[3], err)
	}

	currentBrightness = pct
	return nil
}

// adjustBrightness changes brightness by delta (positive to increase,
// negative to decrease), clamped to 0-100, then syncs hyprsunset to
// match. flags are any extra arguments that followed "up"/"down" and
// weren't the step number — they're forwarded to the underlying
// `brightnessctl set` call verbatim (e.g. -q, -p, -d eDP-1).
//
// After the set call, brightness is re-read from brightnessctl rather
// than trusting the computed value, and hyprsunset is synced to that.
// The set call can succeed (exit 0, no error) without the backlight
// actually changing — e.g. -p/--pretend explicitly skips the write, or
// a stray non-flag token upstream in parseUpDownArgs could in principle
// get consumed as brightnessctl's operation instead of "set". Trusting
// our own guess in either case would resync hyprsunset to a brightness
// the screen never reached, only for the next invocation to correct it.
// Re-reading avoids that class of bug entirely.
func adjustBrightness(delta int, flags []string) {
	if err := getBrightness(); err != nil {
		fmt.Println("error:", err)
		return
	}

	newBrightness = currentBrightness + delta
	if newBrightness > 100 {
		newBrightness = 100
	}
	if newBrightness < 0 {
		newBrightness = 0
	}
	brightnessStr := strconv.Itoa(newBrightness)

	setArgs := append(append([]string{}, flags...), "set", brightnessStr+"%")
	out, err := runCommand("brightnessctl", setArgs...)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(out)

	if err := getBrightness(); err != nil {
		fmt.Println("error syncing hyprsunset:", err)
		return
	}
	if err := syncHyprsunset(currentBrightness); err != nil {
		fmt.Println("error syncing hyprsunset:", err)
	}
}

// passthroughBrightnessctl forwards args to the real brightnessctl
// verbatim and prints whatever it prints. Used for every brightnessctl
// operation/option oledctl doesn't need to special-case itself (info,
// get, max, list, quiet, pretend, machine-readable, min-value, exponent,
// save, restore, device, class, version, and set).
func passthroughBrightnessctl(args []string) error {
	out, err := runCommand("brightnessctl", args...)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// isSetOperation reports whether args contains brightnessctl's "s" or
// "set" operation token, meaning brightness may have changed and
// hyprsunset needs to be resynced afterward. This is a simple token scan
// rather than full option parsing, so it can misfire if "set" is used as
// the literal value of e.g. -d/--device — an acceptably rare edge case,
// since a missed resync just self-corrects on the next brightness change.
func isSetOperation(args []string) bool {
	for _, a := range args {
		if a == "s" || a == "set" {
			return true
		}
	}
	return false
}

func increaseBrightness(step int, flags []string) {
	adjustBrightness(step, flags)
}

func decreaseBrightness(step int, flags []string) {
	adjustBrightness(-step, flags)
}

// parseUpDownArgs walks the arguments after "up"/"down". Each token that
// parses as an integer is used as the step amount (only the first one —
// a second integer is an error, not silently overwritten). Each token
// starting with "-" is treated as a brightnessctl flag and collected to
// forward as-is (e.g. -q, -p, -d eDP-1). Anything else — a bare word
// that's neither an integer nor flag-shaped — is rejected outright:
// forwarding it would let brightnessctl silently consume it as a bogus
// operation instead of "set" (see the observed real brightnessctl
// behavior with an unrecognized operation), which looks like a
// successful write to oledctl but never actually changes anything. If
// no integer is found, step defaults to defaultStep.
func parseUpDownArgs(args []string, defaultStep int) (step int, flags []string, err error) {
	step = defaultStep
	stepSet := false

	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			continue
		}

		n, convErr := strconv.Atoi(a)
		if convErr != nil {
			return 0, nil, fmt.Errorf("invalid argument %q: not an integer step or a recognized flag (flags must start with -)", a)
		}
		if n < 0 {
			return 0, nil, fmt.Errorf("invalid step %q: must not be negative", a)
		}
		if stepSet {
			return 0, nil, fmt.Errorf("unexpected extra step %q (step already set to %d)", a, step)
		}
		step = n
		stepSet = true
	}

	return step, flags, nil
}

const defaultStep = 10

// oledctlHelp is oledctl's own man-page-style help text.
const oledctlHelp = `NAME
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
    status
        Print the current brightness (via brightnessctl -m get).

OPTIONS
    -h, --help
        Print this help.
    -h, --help brightnessctl
        Print the underlying brightnessctl tool's own help text.

PASSED THROUGH TO BRIGHTNESSCTL
    Every other brightnessctl operation and option — i/info, g/get,
    m/max, s/set VALUE, -l/--list, -q/--quiet, -p/--pretend,
    -m/--machine-readable, -n/--min-value, -e/--exponent, -s/--save,
    -r/--restore, -d/--device, -c/--class, -v/--version — is forwarded
    to the real brightnessctl verbatim. Run "oledctl -h brightnessctl"
    for the full description of each. If the forwarded command is a
    set (s/set), hyprsunset is resynced to the resulting brightness
    afterward.

DESCRIPTION
    oledctl works as a middleman between brightnessctl and hyprsunset.
    This keeps hyprsunset's gamma filter in sync with brightnessctl's
    brightness, so it feels like adjusting the same brightness
    brightnessctl reports. This is needed on most OLED displays, which
    lack a backlight and don't expose any way to change the pixel
    brightness directly. hyprsunset's color temperature filter is kept
    permanently disabled (identity) — only its gamma filter is used,
    to isolate brightness from color shift.

    hyprsunset must already be running (e.g. via "exec-once =
    hyprsunset" in hyprland.conf, or its systemd user service) —
    oledctl only talks to it over hyprctl's IPC and never starts or
    stops it itself.

AUTHORS
    See https://github.com/Hummer12007/brightnessctl for
    information about brightnessctl and its source code.
    See https://wiki.hypr.land/Hypr-Ecosystem/hyprsunset/ for
    information about hyprsunset.
    See https://github.com/myuser/oledctl for information about
    oledctl and its source code.
`

// printOledctlHelp prints oledctl's own help text.
func printOledctlHelp() {
	fmt.Print(oledctlHelp)
}

// printBrightnessctlHelp shells out to the real brightnessctl's own
// -h output and prints it verbatim, rather than duplicating it here,
// so it can never drift out of sync with whatever version is installed.
// Captures stdout and stderr together and prints whatever came back
// regardless of exit code, since some builds write help text to
// stderr rather than stdout.
func printBrightnessctlHelp() error {
	realPath, err := findRealBrightnessctl()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, realPath, "-h")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	if out.Len() > 0 {
		fmt.Print(out.String())
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("getting brightnessctl help: %w", runErr)
	}
	return nil
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Println("oledctl: no cmd given")
		return
	}

	switch args[0] {
	case "-h", "--help":
		if len(args) > 1 && args[1] == "brightnessctl" {
			if err := printBrightnessctlHelp(); err != nil {
				fmt.Println("error:", err)
			}
			return
		}
		printOledctlHelp()
	case "status":
		if err := printStatus(); err != nil {
			fmt.Println("error:", err)
			return
		}
	case "up":
		step, flags, err := parseUpDownArgs(args, defaultStep)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		increaseBrightness(step, flags)
	case "down":
		step, flags, err := parseUpDownArgs(args, defaultStep)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		decreaseBrightness(step, flags)
	default:
		// Everything else — i/info, g/get, m/max, s/set, -l, -q, -p,
		// -n, -e, -s, -r, -d, -c, -v, and any combination of these
		// preceding an operation — is real brightnessctl syntax that
		// oledctl doesn't need to special-case, so it's forwarded
		// verbatim to the real binary.
		if err := passthroughBrightnessctl(args); err != nil {
			fmt.Println(err)
			return
		}
		if isSetOperation(args) {
			if err := getBrightness(); err != nil {
				fmt.Println("error syncing hyprsunset:", err)
				return
			}
			if err := syncHyprsunset(currentBrightness); err != nil {
				fmt.Println("error syncing hyprsunset:", err)
			}
		}
	}
}
