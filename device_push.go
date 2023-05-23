package adb

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// PushPath could puth local file or directory to remote
func (c *Device) PushPath(showProgress bool, localPath, remotePath string, handler func(size, total int64)) (err error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return wrapClientError(err, c, "PushWithProgress")
	}
	size := info.Size()
	perms := info.Mode().Perm()
	mtime := info.ModTime()

	if info.IsDir() {
		for {
			filepath.WalkDir(localPath, func(path string, d fs.DirEntry, err error) error {

			})
		}
	} else {
		localFile, err := os.Open(localPath)
		if err != nil {
			return wrapClientError(err, c, "PushWithProgress")
		}
		defer localFile.Close()
		writer, err := c.OpenWrite(remotePath, perms, mtime)
		if err != nil {
			return wrapClientError(err, c, "PushWithProgress")
		}
		defer writer.Close()

		if err := c.copyFile(writer, localFile, size, handler); err != nil {
			fmt.Fprintln(os.Stderr, "error pushing file:", err)
			return wrapClientError(err, c, "PushWithProgress")
		}
	}

	return nil
}

type bufStats struct {
	total   int64
	n       int64
	handler func(size, total int64)
}

func (b *bufStats) Write(p []byte) (n int, err error) {
	b.n += int64(len(p))
	b.handler(b.n, b.total)
	n = len(p)
	return
}

// copyFile copies src to dst.
// If showProgress is true and size is positive, a progress bar is shown.
// After copying, final stats about the transfer speed and size are shown.
// Progress and stats are printed to stderr.
func (c *Device) copyFile(dst io.Writer, src io.Reader, total int64, handler func(size, total int64)) error {
	dst = io.MultiWriter(dst, &bufStats{
		total:   total,
		handler: handler,
	})

	// startTime := time.Now()
	copied, err := io.Copy(dst, src)
	if pathErr, ok := err.(*os.PathError); ok {
		if errno, ok := pathErr.Err.(syscall.Errno); ok && errno == syscall.EPIPE {
			// Pipe closed. Handle this like an EOF.
			err = nil
		}
	}
	if err != nil {
		return err
	}

	_ = copied
	// duration := time.Now().Sub(startTime)
	// rate := int64(float64(copied) / duration.Seconds())
	// fmt.Fprintf(os.Stderr, "%d B/s (%d bytes in %s)\n", rate, copied, duration)
	return nil
}
