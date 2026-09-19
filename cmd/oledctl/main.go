package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

const (
	// Color temperature is always kept at 6500K (neutral/no shift) —
	// only brightness is adjusted via gammastep's -b parameter.
	fixedTemp = "6500K"
)

var (
	currentBrightness int // percentage, 0-100
	newBrightness     int
)

// commandTimeout bounds how long a quick, expected-to-exit external
// command (brightnessctl) is allowed to run. This is NOT used for
// gammastep — see syncGammastep below for why.
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

// gammastep is launched detached and tracked by PID, and killed only when
// a new brightness value needs to be applied. Because oledctl is stateless
// between invocations, that PID is tracked on disk rather than in memory.
func gammastepPIDPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "oledctl-gammastep.pid")
}

// stopPreviousGammastep kills whatever gammastep process oledctl last
// started (if any), tracked via gammastepPIDPath, and waits for it to
// actually exit so the replacement process doesn't race it for the
// gamma-control object. Safe to call even if nothing is tracked, or if
// the tracked process has already died on its own.
func stopPreviousGammastep() {
	pidPath := gammastepPIDPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return // nothing tracked
	}
	defer os.Remove(pidPath)

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return // already gone
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return // exited
		}
		time.Sleep(50 * time.Millisecond)
	}
	// didn't exit cleanly within the grace period — force it, so a stuck
	// gammastep can never accumulate as an orphaned process
	_ = proc.Signal(syscall.SIGKILL)
}

// startGammastep launches gammastep detached from oledctl's own process
// (Setsid, so it survives oledctl exiting and isn't taken down if a
// terminal oledctl was run from closes) and records its PID so a future
// invocation can stop it. This does NOT wait for it to exit — it's meant
// to keep running until explicitly replaced. A background goroutine reaps
// it once it eventually does exit, to avoid a zombie process.
func startGammastep(percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	ratio := float64(percent) / 100.0
	brightArg := fmt.Sprintf("%.2f:%.2f", ratio, ratio) // day:night, kept equal for a manual one-shot

	cmd := exec.Command("gammastep", "-O", fixedTemp, "-b", brightArg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting gammastep: %w", err)
	}

	if err := os.WriteFile(gammastepPIDPath(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return fmt.Errorf("recording gammastep pid: %w", err)
	}

	go cmd.Wait() // reap it when it eventually exits; don't block on it

	return nil
}

// syncGammastep applies the given brightness percentage to the display.
// Temperature is always fixedTemp (6500K) — only the brightness ratio
// changes, e.g. `gammastep -O 6500K -b 0.40:0.40` for 40%. Kills any
// previously-tracked gammastep before starting the new one. Returns
// immediately — does not wait on gammastep exiting.
func syncGammastep(percent int) error {
	stopPreviousGammastep()
	return startGammastep(percent)
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
// wherever we need a 0-100 number to drive gammastep's -b ratio (e.g.
// syncing after a brightness change).
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
// negative to decrease), clamped to 0-100, then syncs gammastep to
// match. flags are any extra arguments that followed "up"/"down" and
// weren't the step number — they're forwarded to the underlying
// `brightnessctl set` call verbatim (e.g. -q, -p, -d eDP-1).
//
// After the set call, brightness is re-read from brightnessctl rather
// than trusting the computed value, and gammastep is synced to that.
// The set call can succeed (exit 0, no error) without the backlight
// actually changing — e.g. -p/--pretend explicitly skips the write, or
// a stray non-flag token upstream in parseUpDownArgs could in principle
// get consumed as brightnessctl's operation instead of "set". Trusting
// our own guess in either case would resync gammastep to a brightness
// the screen never reached, only for the next invocation to correct it
// (a visible flicker). Re-reading avoids that class of bug entirely.
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
		fmt.Println("error syncing gammastep:", err)
		return
	}
	if err := syncGammastep(currentBrightness); err != nil {
		fmt.Println("error syncing gammastep:", err)
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
// gammastep needs to be resynced afterward. This is a simple token scan
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
        gammastep's color temperature to the new brightness. Any
        argument that isn't an integer is treated as a brightnessctl
        parameter and forwarded as-is (e.g. -q, -p, -d eDP-1).
    down [STEP] [param...]
        Decrease brightness by STEP percent (default 10), then resync
        gammastep's color temperature to the new brightness. Same
        argument handling as up.
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
    set (s/set), gammastep is resynced to the resulting brightness
    afterward.

DESCRIPTION
    oledctl works as a middleman between brightnessctl and gammastep.
    This keeps gammastep's color temperature in sync with
    brightnessctl's brightness, so it feels like adjusting the same
    brightness brightnessctl reports. This is needed on most OLED
    displays, which lack a backlight and don't expose any way to
    change the pixel brightness directly.

AUTHORS
    See https://github.com/Hummer12007/brightnessctl for
    information about brightnessctl and its source code.
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
				fmt.Println("error syncing gammastep:", err)
				return
			}
			if err := syncGammastep(currentBrightness); err != nil {
				fmt.Println("error syncing gammastep:", err)
			}
		}
	}
}
