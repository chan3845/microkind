package guest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func logOverlay(format string, a ...any) {
	msg := fmt.Sprintf("[overlay-init] "+format+"\n", a...)
	os.Stdout.WriteString(msg)
	// Also attempt writing directly to console if stdout is somehow redirected
	if f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		f.WriteString(msg)
		f.Close()
	}
}

// RunOverlayInit prepares the zero-copy overlay rootfs and execs systemd
func RunOverlayInit() error {
	logOverlay("Starting fckind zero-copy overlay initialization (PID %d)...", os.Getpid())

	// 1. Mount virtual kernel filesystems
	if err := mountVirtualFS(); err != nil {
		logOverlay("Error mounting virtual filesystems: %v", err)
		return err
	}

	// 2. Locate overlay block device (/dev/vdb)
	const overlayDev = "/dev/vdb"
	logOverlay("Waiting for overlay device %s...", overlayDev)
	if err := waitForDevice(overlayDev, 5*time.Second); err != nil {
		logOverlay("Overlay device %s not found: %v. Falling back to direct init.", overlayDev, err)
		return execSystemd()
	}

	// 3. Verify or format /dev/vdb as ext4
	if err := ensureExt4(overlayDev); err != nil {
		logOverlay("Failed to ensure ext4 on %s: %v", overlayDev, err)
		return err
	}

	// 4. Mount /dev/vdb on staging path /run/overlay_backing
	const stagingBacking = "/run/overlay_backing"
	const newRoot = "/run/newroot"

	if err := os.MkdirAll(stagingBacking, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", stagingBacking, err)
	}

	logOverlay("Mounting %s to %s...", overlayDev, stagingBacking)
	if err := unix.Mount(overlayDev, stagingBacking, "ext4", 0, "noatime"); err != nil {
		return fmt.Errorf("failed to mount %s on %s: %w", overlayDev, stagingBacking, err)
	}

	upperDir := filepath.Join(stagingBacking, "upper")
	workDir := filepath.Join(stagingBacking, "work")
	if err := os.MkdirAll(upperDir, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", upperDir, err)
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", workDir, err)
	}

	// 5. Mount overlayfs at /run/newroot
	if err := os.MkdirAll(newRoot, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", newRoot, err)
	}

	overlayOpts := fmt.Sprintf("lowerdir=/,upperdir=%s,workdir=%s", upperDir, workDir)
	logOverlay("Mounting overlayfs at %s...", newRoot)
	if err := unix.Mount("overlay", newRoot, "overlay", 0, overlayOpts); err != nil {
		return fmt.Errorf("failed to mount overlayfs: %w", err)
	}

	// 6. Move the backing filesystem into newRoot so it remains mounted
	finalBacking := filepath.Join(newRoot, "run", "overlay_backing")
	if err := os.MkdirAll(finalBacking, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", finalBacking, err)
	}
	if err := unix.Mount(stagingBacking, finalBacking, "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("failed to move overlay backing mount: %w", err)
	}

	// 7. Move virtual filesystems into newRoot
	for _, dir := range []string{"/dev", "/proc", "/sys"} {
		target := filepath.Join(newRoot, dir)
		if err := os.MkdirAll(target, 0755); err != nil {
			return fmt.Errorf("failed to mkdir %s: %w", target, err)
		}
		if err := unix.Mount(dir, target, "", unix.MS_MOVE, ""); err != nil {
			logOverlay("Warning: failed to move %s to %s: %v", dir, target, err)
		}
	}

	// 8. Pivot/Switch root to newRoot
	logOverlay("Switching root to overlayfs...")
	if err := switchRoot(newRoot); err != nil {
		return fmt.Errorf("failed to switch root: %w", err)
	}

	// 9. Exec systemd
	logOverlay("Executing /sbin/init on merged rootfs...")
	return execSystemd()
}

func mountVirtualFS() error {
	for _, m := range []struct {
		dev, dir, fstype string
		flags            uintptr
		data             string
	}{
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV, ""},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
	} {
		_ = os.MkdirAll(m.dir, 0755)
		if err := unix.Mount(m.dev, m.dir, m.fstype, m.flags, m.data); err != nil {
			// Ignore if already mounted
			if err != unix.EBUSY {
				logOverlay("Warning: mount %s on %s error: %v", m.dev, m.dir, err)
			}
		}
	}
	return nil
}

func waitForDevice(dev string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dev); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return os.ErrNotExist
}

func ensureExt4(dev string) error {
	f, err := os.Open(dev)
	if err != nil {
		return fmt.Errorf("open %s: %w", dev, err)
	}
	defer f.Close()

	buf := make([]byte, 2)
	// Ext4 superblock magic is 0xef53 at offset 1080 (1024 + 56)
	if _, err := f.ReadAt(buf, 1080); err == nil {
		if bytes.Equal(buf, []byte{0x53, 0xef}) {
			logOverlay("Device %s already has valid ext4 filesystem.", dev)
			return nil
		}
	}

	logOverlay("Formatting %s with mkfs.ext4...", dev)
	cmd := exec.Command("/sbin/mkfs.ext4", "-F", "-m", "0", dev)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Try mke2fs fallback
		cmdFallback := exec.Command("mke2fs", "-t", "ext4", "-F", dev)
		if err2 := cmdFallback.Run(); err2 != nil {
			return fmt.Errorf("mkfs.ext4 failed: %w (fallback: %v)", err, err2)
		}
	}
	logOverlay("Formatted %s successfully.", dev)
	return nil
}

func switchRoot(newRoot string) error {
	if err := unix.Chdir(newRoot); err != nil {
		return fmt.Errorf("chdir %s: %w", newRoot, err)
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("mount move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	return nil
}

func execSystemd() error {
	initCandidates := []string{
		"/sbin/init",
		"/lib/systemd/systemd",
		"/usr/lib/systemd/systemd",
	}

	for _, initPath := range initCandidates {
		if _, err := os.Stat(initPath); err == nil {
			env := append(os.Environ(), "container=firecracker", "HOME=/root", "TERM=linux")
			return syscall.Exec(initPath, []string{initPath}, env)
		}
	}

	return fmt.Errorf("no valid init binary found among %v", initCandidates)
}
