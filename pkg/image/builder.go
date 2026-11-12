package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Builder builds and caches Firecracker Alpine+k0s node images
type Builder struct {
	K0sVersion  string
	Airgap      bool
	RegistryURL string
	CacheDir    string
}

// NewBuilder creates a new image builder
func NewBuilder(k0sVersion string, airgap bool) (*Builder, error) {
	if k0sVersion == "" {
		k0sVersion = "v1.37.2+k0s.0"
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/ubuntu"
	}
	cacheDir := filepath.Join(home, ".cache", "fckind", "images")

	return &Builder{
		K0sVersion:  k0sVersion,
		Airgap:      airgap,
		RegistryURL: "localhost:5000",
		CacheDir:    cacheDir,
	}, nil
}

// ImageTag returns the full OCI image tag, e.g. localhost:5000/fckind/node:v1.37.2-k0s.0
func (b *Builder) ImageTag() string {
	sanitizedVer := strings.ReplaceAll(b.K0sVersion, "+", "-")
	return fmt.Sprintf("%s/fckind/node:%s", b.RegistryURL, sanitizedVer)
}

// TargetCacheDir returns the directory containing cached vmlinux and rootfs.ext4
func (b *Builder) TargetCacheDir() string {
	sanitizedVer := strings.ReplaceAll(b.K0sVersion, "+", "-")
	return filepath.Join(b.CacheDir, sanitizedVer)
}

// BuildNodeImage builds the Docker image and packages rootfs.ext4 + vmlinux
func (b *Builder) BuildNodeImage(ctx context.Context, repoRoot string) error {
	imageTag := b.ImageTag()
	dockerfilePath := filepath.Join(repoRoot, "images", "node", "Dockerfile.alpine")
	targetDir := b.TargetCacheDir()

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create cache dir %s: %w", targetDir, err)
	}

	// 1. Build Docker image
	airgapArg := fmt.Sprintf("AIRGAP=%t", b.Airgap)
	k0sArg := fmt.Sprintf("K0S_VERSION=%s", b.K0sVersion)

	fmt.Printf("[image-builder] Building node image %s...\n", imageTag)
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"--network=host",
		"-t", imageTag,
		"-f", dockerfilePath,
		"--build-arg", airgapArg,
		"--build-arg", k0sArg,
		repoRoot,
	)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	// 2. Push to local registry
	fmt.Printf("[image-builder] Pushing %s to local registry...\n", imageTag)
	pushCmd := exec.CommandContext(ctx, "docker", "push", imageTag)
	pushCmd.Stdout = os.Stdout
	pushCmd.Stderr = os.Stderr
	if err := pushCmd.Run(); err != nil {
		fmt.Printf("Warning: docker push failed: %v (continuing with local image)\n", err)
	}

	// 3. Extract rootfs.ext4 and vmlinux
	return b.ExtractRootfs(ctx, imageTag, targetDir)
}

// ExtractRootfs creates a sparse rootfs.ext4 and copies vmlinux from the container
func (b *Builder) ExtractRootfs(ctx context.Context, imageTag string, targetDir string) error {
	rootfsPath := filepath.Join(targetDir, "rootfs.ext4")
	vmlinuxPath := filepath.Join(targetDir, "vmlinux")

	fmt.Printf("[image-builder] Extracting rootfs.ext4 and vmlinux to %s...\n", targetDir)

	// Create a temporary container
	createCmd := exec.CommandContext(ctx, "docker", "create", imageTag)
	out, err := createCmd.Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	defer func() {
		_ = exec.Command("docker", "rm", "-f", containerID).Run()
	}()

	// Copy out /boot/vmlinux
	cpKernelCmd := exec.CommandContext(ctx, "docker", "cp", fmt.Sprintf("%s:/boot/vmlinux", containerID), vmlinuxPath)
	if err := cpKernelCmd.Run(); err != nil {
		// Fallback: check repo bin/vmlinux
		repoKernel := "/home/ubuntu/fckind/bin/vmlinux"
		if _, err2 := os.Stat(repoKernel); err2 == nil {
			_ = exec.Command("cp", repoKernel, vmlinuxPath).Run()
		} else {
			return fmt.Errorf("extract vmlinux from container: %w", err)
		}
	}

	// Allocate sparse rootfs.ext4 (4 GB virtual capacity, occupies minimal actual blocks)
	_ = os.Remove(rootfsPath)
	if err := exec.Command("truncate", "-s", "4G", rootfsPath).Run(); err != nil {
		return fmt.Errorf("create sparse rootfs.ext4: %w", err)
	}

	// Format ext4 filesystem
	if out, err := exec.Command("mkfs.ext4", "-F", "-m", "0", rootfsPath).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 rootfs: %w (%s)", err, string(out))
	}

	// Mount staging directory and unpack container contents
	stagingMount := filepath.Join(os.TempDir(), "fckind-rootfs-staging")
	_ = os.MkdirAll(stagingMount, 0755)
	defer os.RemoveAll(stagingMount)

	mountCmd := exec.Command("sudo", "mount", "-o", "loop", rootfsPath, stagingMount)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount rootfs staging: %w (%s)", err, string(out))
	}

	// Export container tarball directly into staging mount using robust shell pipeline
	pipeCmd := fmt.Sprintf("docker export %s | sudo tar -C %s -xf -", containerID, stagingMount)
	unpackCmd := exec.CommandContext(ctx, "sh", "-c", pipeCmd)
	if out, err := unpackCmd.CombinedOutput(); err != nil {
		_ = exec.Command("sudo", "umount", stagingMount).Run()
		return fmt.Errorf("unpack container rootfs: %w (%s)", err, string(out))
	}

	// Sync and unmount
	_ = exec.Command("sync").Run()
	if out, err := exec.Command("sudo", "umount", stagingMount).CombinedOutput(); err != nil {
		return fmt.Errorf("umount rootfs staging: %w (%s)", err, string(out))
	}

	fmt.Printf("[image-builder] Successfully created cached rootfs at %s\n", rootfsPath)
	return nil
}
