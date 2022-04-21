package adb

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/zach-klippenstein/goadb/internal/errors"
	"github.com/zach-klippenstein/goadb/wire"
)

// MtimeOfClose should be passed to OpenWrite to set the file modification time to the time the Close
// method is called.
var MtimeOfClose = time.Time{}

// Device communicates with a specific Android device.
// To get an instance, call Device() on an Adb.
type Device struct {
	server     server
	descriptor DeviceDescriptor

	// Used to get device info.
	deviceListFunc func() ([]*DeviceInfo, error)
}

func (c *Device) String() string {
	return c.descriptor.String()
}

// get-product is documented, but not implemented, in the server.
// TODO(z): Make product exported if get-product is ever implemented in adb.
func (c *Device) product() (string, error) {
	attr, err := c.getAttribute("get-product")
	return attr, wrapClientError(err, c, "Product")
}

func (c *Device) Serial() (string, error) {
	attr, err := c.getAttribute("get-serialno")
	return attr, wrapClientError(err, c, "Serial")
}

func (c *Device) DevicePath() (string, error) {
	attr, err := c.getAttribute("get-devpath")
	return attr, wrapClientError(err, c, "DevicePath")
}

func (c *Device) State() (DeviceState, error) {
	attr, err := c.getAttribute("get-state")
	if err != nil {
		if strings.Contains(err.Error(), "unauthorized") {
			return StateUnauthorized, nil
		}
		return StateInvalid, wrapClientError(err, c, "State")
	}
	state, err := parseDeviceState(attr)
	return state, wrapClientError(err, c, "State")
}

func (c *Device) DeviceInfo() (*DeviceInfo, error) {
	// Adb doesn't actually provide a way to get this for an individual device,
	// so we have to just list devices and find ourselves.

	serial, err := c.Serial()
	if err != nil {
		return nil, wrapClientError(err, c, "GetDeviceInfo(GetSerial)")
	}

	devices, err := c.deviceListFunc()
	if err != nil {
		return nil, wrapClientError(err, c, "DeviceInfo(ListDevices)")
	}

	for _, deviceInfo := range devices {
		if deviceInfo.Serial == serial {
			return deviceInfo, nil
		}
	}

	err = errors.Errorf(errors.DeviceNotFound, "device list doesn't contain serial %s", serial)
	return nil, wrapClientError(err, c, "DeviceInfo")
}

/*
RunCommand runs the specified commands on a shell on the device.

From the Android docs:
	Run 'command arg1 arg2 ...' in a shell on the device, and return
	its output and error streams. Note that arguments must be separated
	by spaces. If an argument contains a space, it must be quoted with
	double-quotes. Arguments cannot contain double quotes or things
	will go very wrong.

	Note that this is the non-interactive version of "adb shell"
Source: https://android.googlesource.com/platform/system/core/+/master/adb/SERVICES.TXT

This method quotes the arguments for you, and will return an error if any of them
contain double quotes.
*/
func (c *Device) RunCommand(cmd string, args ...string) (string, error) {
	cmd, err := prepareCommandLine(cmd, args...)
	if err != nil {
		return "", wrapClientError(err, c, "RunCommand")
	}

	conn, err := c.dialDevice()
	if err != nil {
		return "", wrapClientError(err, c, "RunCommand")
	}
	defer conn.Close()

	req := fmt.Sprintf("shell:%s", cmd)

	// Shell responses are special, they don't include a length header.
	// We read until the stream is closed.
	// So, we can't use conn.RoundTripSingleResponse.
	if err = conn.SendMessage([]byte(req)); err != nil {
		return "", wrapClientError(err, c, "RunCommand")
	}
	if _, err = conn.ReadStatus(req); err != nil {
		return "", wrapClientError(err, c, "RunCommand")
	}

	resp, err := conn.ReadUntilEof()
	return string(resp), wrapClientError(err, c, "RunCommand")
}

/*
Remount, from the official adb command’s docs:
	Ask adbd to remount the device's filesystem in read-write mode,
	instead of read-only. This is usually necessary before performing
	an "adb sync" or "adb push" request.
	This request may not succeed on certain builds which do not allow
	that.
Source: https://android.googlesource.com/platform/system/core/+/master/adb/SERVICES.TXT
*/
func (c *Device) Remount() (string, error) {
	conn, err := c.dialDevice()
	if err != nil {
		return "", wrapClientError(err, c, "Remount")
	}
	defer conn.Close()

	resp, err := conn.RoundTripSingleResponse([]byte("remount"))
	return string(resp), wrapClientError(err, c, "Remount")
}

func (c *Device) ListDirEntries(path string) (*DirEntries, error) {
	conn, err := c.getSyncConn()
	if err != nil {
		return nil, wrapClientError(err, c, "ListDirEntries(%s)", path)
	}

	entries, err := listDirEntries(conn, path)
	return entries, wrapClientError(err, c, "ListDirEntries(%s)", path)
}

func (c *Device) Stat(path string) (*DirEntry, error) {
	conn, err := c.getSyncConn()
	if err != nil {
		return nil, wrapClientError(err, c, "Stat(%s)", path)
	}
	defer conn.Close()

	entry, err := stat(conn, path)
	return entry, wrapClientError(err, c, "Stat(%s)", path)
}

func (c *Device) OpenRead(path string) (io.ReadCloser, error) {
	conn, err := c.getSyncConn()
	if err != nil {
		return nil, wrapClientError(err, c, "OpenRead(%s)", path)
	}

	reader, err := receiveFile(conn, path)
	return reader, wrapClientError(err, c, "OpenRead(%s)", path)
}

// OpenWrite opens the file at path on the device, creating it with the permissions specified
// by perms if necessary, and returns a writer that writes to the file.
// The files modification time will be set to mtime when the WriterCloser is closed. The zero value
// is TimeOfClose, which will use the time the Close method is called as the modification time.
func (c *Device) OpenWrite(path string, perms os.FileMode, mtime time.Time) (io.WriteCloser, error) {
	conn, err := c.getSyncConn()
	if err != nil {
		return nil, wrapClientError(err, c, "OpenWrite(%s)", path)
	}

	writer, err := sendFile(conn, path, perms, mtime)
	return writer, wrapClientError(err, c, "OpenWrite(%s)", path)
}

// getAttribute returns the first message returned by the server by running
// <host-prefix>:<attr>, where host-prefix is determined from the DeviceDescriptor.
func (c *Device) getAttribute(attr string) (string, error) {
	resp, err := roundTripSingleResponse(c.server,
		fmt.Sprintf("%s:%s", c.descriptor.getHostPrefix(), attr))
	if err != nil {
		return "", err
	}
	return string(resp), nil
}

func (c *Device) getSyncConn() (*wire.SyncConn, error) {
	conn, err := c.dialDevice()
	if err != nil {
		return nil, err
	}

	// Switch the connection to sync mode.
	if err := wire.SendMessageString(conn, "sync:"); err != nil {
		return nil, err
	}
	if _, err := conn.ReadStatus("sync"); err != nil {
		return nil, err
	}

	return conn.NewSyncConn(), nil
}

// dialDevice switches the connection to communicate directly with the device
// by requesting the transport defined by the DeviceDescriptor.
func (c *Device) dialDevice() (*wire.Conn, error) {
	conn, err := c.server.Dial()
	if err != nil {
		return nil, err
	}

	req := fmt.Sprintf("host:%s", c.descriptor.getTransportDescriptor())
	if err = wire.SendMessageString(conn, req); err != nil {
		conn.Close()
		return nil, errors.WrapErrf(err, "error connecting to device '%s'", c.descriptor)
	}

	if _, err = conn.ReadStatus(req); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

// prepareCommandLine validates the command and argument strings, quotes
// arguments if required, and joins them into a valid adb command string.
func prepareCommandLine(cmd string, args ...string) (string, error) {
	if isBlank(cmd) {
		return "", errors.AssertionErrorf("command cannot be empty")
	}

	for i, arg := range args {
		if strings.ContainsRune(arg, '"') {
			return "", errors.Errorf(errors.ParseError, "arg at index %d contains an invalid double quote: %s", i, arg)
		}
		if containsWhitespace(arg) {
			//args[i] = fmt.Sprintf("\"%s\"", arg)
		}
	}

	// Prepend the command to the args array.
	if len(args) > 0 {
		cmd = fmt.Sprintf("%s %s", cmd, strings.Join(args, " "))
	}

	return cmd, nil
}

// run adb cmd string
// Use "\ " instead of " " like shell
func (c *Device) RunAdbCmd(cmd string) (string, error) {
	// cmdArgs := strings.Split(cmd, " ")
	cmdArgs := splitCmdAgrs(cmd)
	adbPath, _ := exec.LookPath(AdbExecutableName)
	result, err := exec.Command(adbPath, cmdArgs...).Output()
	return string(result), err
}

// run adb cmd string
// Use "\ " instead of " " like shell
func (c *Device) RunAdbCmdCtx(ctx context.Context, cmd string) (string, error) {
	// cmdArgs := strings.Split(cmd, " ")
	cmdArgs := splitCmdAgrs(cmd)
	adbPath, _ := exec.LookPath(AdbExecutableName)
	runCmd := exec.CommandContext(ctx, adbPath, cmdArgs...)
	result, err := runCmd.Output()
	return string(result), err
}

// run adb cmd string
// Use "\ " instead of " " like shell
func (c *Device) RunAdbShellCmdCtx(ctx context.Context, cmd string) (string, error) {
	// cmdArgs := strings.Split(cmd, " ")
	cmdArgs := splitCmdAgrs("-s " + c.descriptor.serial + " shell " + cmd)
	adbPath, _ := exec.LookPath(AdbExecutableName)
	runCmd := exec.CommandContext(ctx, adbPath, cmdArgs...)
	result, err := runCmd.Output()
	return string(result), err
}

// Push file
func (c *Device) Push(localPath, remotePath string) (string, error) {
	var args string
	args += " " + safeArg(strings.TrimSpace(localPath)) + " " + safeArg(strings.TrimSpace(remotePath))
	result, isError := c.RunAdbCmd("-s " + c.descriptor.serial + " push" + args)
	return result, isError
}

// Forward
func (c *Device) Forward(localPort, remotePort string) (string, error) {
	var args string
	args += " " + safeArg(strings.TrimSpace(localPort)) + " " + safeArg(strings.TrimSpace(remotePort))
	result, isError := c.RunAdbCmd("-s " + c.descriptor.serial + " forward" + args)
	return result, isError
}

// ClearForward
func (c *Device) ClearForwardAll() (string, error) {
	var args string
	args += " " + "--remove-all"
	result, isError := c.RunAdbCmd("-s " + c.descriptor.serial + " forward" + args)
	return result, isError
}

// GetForwardList
func (c *Device) GetForwardList(localPort, remotePort string) (string, error) {
	var args string
	args += " " + " --list "
	result, isError := c.RunAdbCmd("-s " + c.descriptor.serial + " forward" + args)
	return result, isError
}

// ClearForward by Serial
func (c *Device) ClearForwardBySerial(deviceId string, port int, remote string) (string, error) {
	forwardStr, err := c.GetForwardList(deviceId, remote)
	if err != nil {
		return "", err
	}
	var args string
	forwardStrList := strings.Split(forwardStr, "\n")
	for _, forwardLine := range forwardStrList {
		if strings.TrimSpace(forwardLine) == "" || !strings.HasPrefix(forwardLine, deviceId) {
			continue
		}
		forwardParams := strings.Fields(forwardLine)
		if len(forwardParams) < 2 {
			continue
		}
		if port > 0 && port < 65535 && !strings.Contains(forwardParams[1], fmt.Sprintf("tcp:%d", port)) {
			continue
		}
		if remote != "" && !strings.Contains(forwardParams[2], "localabstract:"+remote) &&
			!strings.Contains(forwardParams[2], "tcp:"+remote) {
			continue
		}
		args += " " + " --remove " + forwardParams[1]
	}
	result, isError := c.RunAdbCmd("-s " + c.descriptor.serial + " forward" + args)
	return result, isError
}

// InstallApp TODO:connect to adb server
func (c *Device) InstallApp(ctx context.Context, apk string, reinstall bool, grantPermission bool) (string, error) {
	var args string
	args += " " + safeArg(strings.TrimSpace(apk))
	if reinstall {
		args += " -r "
	}

	if grantPermission {
		args += " -g "
	}

	result, isError := c.RunAdbCmdCtx(ctx, "-s " + c.descriptor.serial + " install" + args)
	return result, isError
}

// InstallApp TODO:connect to adb server
func (c *Device) InstallAppByPm(ctx context.Context, apk string, reinstall bool, grantPermission bool) (string, error) {
	var args string
	if reinstall {
		args += " -r "
	}

	if grantPermission {
		args += " -g "
	}

	args += " " + safeArg(strings.TrimSpace(apk))

	result, isError := c.RunAdbCmdCtx(ctx, "-s " + c.descriptor.serial + " shell pm install " + args)
	return result, isError
}

// UninstallApp TODO:connect to adb server
func (c *Device) UninstallApp(ctx context.Context, pkg string) (string, error) {
	var args string
	args += " " + safeArg(strings.TrimSpace(pkg))
	result, isError := c.RunAdbCmdCtx(ctx, "-s " + c.descriptor.serial + " uninstall " + args)
	return result, isError
}

// LaunchApk
func (c *Device) LaunchApk(pkg string) (string, error) {
	temps := fmt.Sprintf("start -n %s", pkg)
	result, isError := c.RunCommand("am", temps)
	return result, isError
}

// Click 436,1291
func (c *Device) Click(x, y int) (string, error) {
	temps := fmt.Sprintf("tap %d %d", x, y)
	result, isError := c.RunCommand("input", temps)
	return result, isError
}

// Drag 436,1291 -> 636,1291
func (c *Device) Drag(x, y, x1, y1 int) (string, error) {
	temps := fmt.Sprintf("swipe %d %d %d %d", x, y, x1, y1)
	result, isError := c.RunCommand("input", temps)
	return result, isError
}

// Home back to home
func (c *Device) Home() (string, error) {
	result, isError := c.RunCommand("input", "keyevent 3")
	return result, isError
}

// Home back to home
func (c *Device) InputText(text string) (string, error) {
	temps := fmt.Sprintf("text %s", text)
	result, isError := c.RunCommand("input", temps)
	return result, isError
}

// Home back to home
func (c *Device) ScreenShot() ([]byte, error) {
	temps := fmt.Sprintf("-p")
	//result, isError := c.RunCommand("screencap", temps)
	cmd, err := prepareCommandLine("screencap", temps)
	if err != nil {
		return nil, wrapClientError(err, c, "RunCommand")
	}

	conn, err := c.dialDevice()
	if err != nil {
		return nil, wrapClientError(err, c, "RunCommand")
	}
	defer conn.Close()

	req := fmt.Sprintf("shell:%s", cmd)

	// Shell responses are special, they don't include a length header.
	// We read until the stream is closed.
	// So, we can't use conn.RoundTripSingleResponse.
	if err = conn.SendMessage([]byte(req)); err != nil {
		return nil, wrapClientError(err, c, "RunCommand")
	}
	if _, err = conn.ReadStatus(req); err != nil {
		return nil, wrapClientError(err, c, "RunCommand")
	}

	resp, err := conn.ReadUntilEof()
	return resp, wrapClientError(err, c, "RunCommand")
}

const StdIoFilename = "-"

type PushEvent struct {
	Current int64
	Total   int64
	Speed   string
	Raw     string
}

func (c *Device) PushWithProgress(ctx context.Context, showProgress bool, localPath, remotePath string, cb func(event PushEvent)) error {
	if remotePath == "" {
		return wrapClientError(errors.WrapErrf(nil,"error: must specify remote file"),
			c, "PushWithProgress")
	}

	var (
		localFile io.ReadCloser
		size      int
		perms     os.FileMode
		mtime     time.Time
	)
	if localPath == "" || localPath == StdIoFilename {
		localFile = os.Stdin
		// 0 size will hide the progress bar.
		perms = os.FileMode(0660)
		mtime = MtimeOfClose
	} else {
		var err error
		localFile, err = os.Open(localPath)
		if err != nil {
			return wrapClientError(err, c, "PushWithProgress")
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return wrapClientError(err, c, "PushWithProgress")
		}
		size = int(info.Size())
		perms = info.Mode().Perm()
		mtime = info.ModTime()
	}
	defer localFile.Close()

	writer, err := c.OpenWrite(remotePath, perms, mtime)
	if err != nil {
		return wrapClientError(err, c, "PushWithProgress")
	}
	defer writer.Close()

	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				writer.Close()
			}
		}()
	}

	if err := c.copyWithProgressAndStats(writer, localFile, size, showProgress, cb); err != nil {
		fmt.Fprintln(os.Stderr, "error pushing file:", err)
		return wrapClientError(err, c, "PushWithProgress")
	}
	return nil
}

// copyWithProgressAndStats copies src to dst.
// If showProgress is true and size is positive, a progress bar is shown.
// After copying, final stats about the transfer speed and size are shown.
// Progress and stats are printed to stderr.
func (c *Device) copyWithProgressAndStats(dst io.Writer, src io.Reader, size int, showProgress bool, cb func(event PushEvent)) error {
	var progress *pb.ProgressBar
	if showProgress && size > 0 && cb != nil {
		progress = pb.New(size)
		progress.Callback = func(out string) {
			fmt.Println("current", progress.Add64(0))

			cb(PushEvent{
				Current: progress.Add64(0),
				Total:   progress.Total,
				Speed:   strings.TrimSpace(out),
			})
		}
		progress.ShowSpeed = true

		progress.ShowCounters = false
		progress.ShowPercent = false
		progress.ShowTimeLeft = false
		progress.ShowElapsedTime = false
		progress.ShowFinalTime = false
		progress.ShowBar = false

		progress.SetUnits(pb.U_BYTES_DEC)
		progress.Start()

		dst = io.MultiWriter(dst, progress)
	}

	startTime := time.Now()
	copied, err := io.Copy(dst, src)

	if progress != nil {
		progress.Finish()
	}

	if pathErr, ok := err.(*os.PathError); ok {
		if errno, ok := pathErr.Err.(syscall.Errno); ok && errno == syscall.EPIPE {
			// Pipe closed. Handle this like an EOF.
			err = nil
		}
	}
	if err != nil {
		return err
	}

	duration := time.Now().Sub(startTime)
	rate := int64(float64(copied) / duration.Seconds())
	fmt.Fprintf(os.Stderr, "%d B/s (%d bytes in %s)\n", rate, copied, duration)

	return nil
}