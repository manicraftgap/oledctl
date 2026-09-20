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
	// Explicit override wins
	if envPath := os.Getenv("REAL_BRIGHTNESSCTL"); envPath != "" {
		return envPath, nil
	}

	// Search PATH, skipping ourselves to avoid recursion
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

// All external calls are quick synchronous IPC, so one timeout fits all.
const commandTimeout = 5 * time.Second

func runCommand(name string, args ...string) (string, error) {
	// Route internal brightnessctl calls to the real binary
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

// clampPercent constrains to hyprsunset's default gamma range (0-100).
func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// syncHyprsunset pushes brightness to hyprsunset's gamma filter, keeping
// color temperature fixed at identity so only brightness changes.
//
// Unlike gammastep, oledctl never starts/stops hyprsunset — it's a
// long-lived daemon (exec-once or systemd) that oledctl just updates over
// IPC. That means no gamma-control handoff and no blink on brightness
// change. If hyprsunset isn't running, these calls fail fast and the
// error surfaces to the user.
func syncHyprsunset(percent int) error {
	if _, err := runCommand("hyprctl", "hyprsunset", "identity"); err != nil {
		return fmt.Errorf("disabling hyprsunset color temperature filter: %w (is hyprsunset running?)", err)
	}

	if _, err := runCommand("hyprctl", "hyprsunset", "gamma", strconv.Itoa(clampPercent(percent))); err != nil {
		return fmt.Errorf("setting hyprsunset gamma: %w (is hyprsunset running?)", err)
	}

	return nil
}

// printStatus prints brightnessctl's own status line verbatim.
func printStatus() error {
	out, err := runCommand("brightnessctl", "-m", "get")
	if err != nil {
		return fmt.Errorf("reading brightness: %w", err)
	}
	fmt.Print(out)
	return nil
}

// getBrightness refreshes currentBrightness from brightnessctl.
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

// adjustBrightness changes brightness by delta (clamped 0-100) and syncs
// hyprsunset to match. flags are extra args forwarded to `brightnessctl
// set` verbatim (e.g. -q, -p, -d eDP-1).
//
// Brightness is re-read after the set rather than trusted from the
// computed value, since a set call can succeed without changing anything
// (e.g. -p/--pretend, or a misparsed token). Re-reading avoids syncing
// hyprsunset to a brightness the screen never reached.
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
// verbatim (info, get, max, list, quiet, pretend, etc.).
func passthroughBrightnessctl(args []string) error {
	out, err := runCommand("brightnessctl", args...)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// isSetOperation reports whether args contains "s"/"set". Simple token
// scan, not full parsing, so it can misfire if "set" is a flag's literal
// value — rare, and self-corrects on the next brightness change.
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

// parseUpDownArgs parses args after "up"/"down": the first integer token
// is the step (a second is an error), "-"-prefixed tokens are forwarded
// flags, and anything else is rejected — otherwise it could get consumed
// by brightnessctl as a bogus operation instead of "set", silently doing
// nothing. Defaults to defaultStep if no integer is given.
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
    See https://github.com/manicraftgap/oledctl/tree/hyprsunset for
    information about this hyprsunset-based branch of oledctl.
`

func printOledctlHelp() {
	fmt.Print(oledctlHelp)
}

// printBrightnessctlHelp shells out to the real brightnessctl's -h so it
// can never drift from the installed version. Captures stdout+stderr
// together since some builds print help to stderr.
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
		// Anything else is real brightnessctl syntax; forward it as-is.
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
