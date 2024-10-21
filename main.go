package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"syscall"
)

func downloadFile(url string, filepath string) error {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("error while downloading file: %w", err)
	}
	defer resp.Body.Close()

	// Check if the HTTP response was successful
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("error while creating file: %w", err)
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("error while writing to file: %w", err)
	}

	return nil
}

// convertQCOW2ToRaw converts a QCOW2 image to a raw image using qemu-img
func convertQCOW2ToRaw(qcow2File, rawFile string) error {
	// Command: qemu-img convert -f qcow2 -O raw qcow2File rawFile
	cmd := exec.Command("qemu-img", "convert", "-f", "qcow2", "-O", "raw", qcow2File, rawFile)

	// Run the command and capture any error
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to convert qcow2 to raw: %w: %s", err, output)
	}
	return nil
}

// attachLoopDevice attaches a raw image to a loop device using losetup
func attachLoopDevice(rawImage string) (string, error) {
	// Command: losetup --find --show rawImage
	cmd := exec.Command("losetup", "--find", "--show", "--partscan", rawImage)

	// Capture the output (the loop device)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to attach loop device: %w", err)
	}

	// Return the loop device (removing any trailing newlines)
	loopDevice := string(output)
	return loopDevice[:len(loopDevice)-1], nil
}

// mountLoopDevice mounts the loop device to the given mount path
func mountLoopDevice(loopDevice, mountPath string) error {
	rootPartition := fmt.Sprintf("%sp1", loopDevice)
	// Command: mount loopDevice mountPath
	cmd := exec.Command("mount", rootPartition, mountPath)

	// Run the mount command
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount loop device: %w: %s", err, output)
	}

	if runtime.GOARCH != "arm64" {
		xboot := fmt.Sprintf("%sp16", loopDevice)
		cmd = exec.Command("mount", xboot, path.Join(mountPath, "/boot"))
	}

	// Run the mount command
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount loop device: %w: %s", err, output)
	}

	esp := fmt.Sprintf("%sp15", loopDevice)
	cmd = exec.Command("mount", esp, path.Join(mountPath, "/boot/efi"))

	// Run the mount command
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount loop device: %w: %s", err, output)
	}

	return nil
}

// unmountLoopDevice unmounts the loop device from the given mount path
func unmountLoopDevice(mountPath string) error {
	// Command: umount mountPath
	cmd := exec.Command("umount", "-R", mountPath)

	// Run the unmount command
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unmount loop device: %w: %s", err, output)
	}

	return nil
}

// detachLoopDevice detaches the loop device from the system using losetup -d
func detachLoopDevice(loopDevice string) error {
	// Command: losetup -d loopDevice
	cmd := exec.Command("losetup", "-d", loopDevice)

	// Run the detach command
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to detach loop device: %w", err)
	}

	return nil
}

func configureCloudInit(mountPath, cloudInitConfigPath string) error {
	filePath := path.Join(mountPath, "/etc/cloud/cloud.cfg.d/chosi.cfg")

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error when creating file: %w", err)
	}

	cloudInitConfig, err := os.Open(cloudInitConfigPath)
	if err != nil {
		return fmt.Errorf("error when opening cloud-init config: %w", err)
	}
	defer cloudInitConfig.Close()

	io.Copy(file, cloudInitConfig)

	// check for error as it would prevent the unmount
	err = file.Close()
	if err != nil {
		return fmt.Errorf("error when closing file: %w", err)
	}

	return nil
}

func installExtraPackages(mountPath string, packages []string) error {
	tmpDirPath := "/tmp/packages"
	absTmpDirPath := path.Join(mountPath, "/tmp/packages")
	err := os.Mkdir(absTmpDirPath, 0777)
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(absTmpDirPath)

	for _, pkg := range packages {
		cmd := exec.Command("cp", pkg, absTmpDirPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed copy package: %w: %s", err, output)
		}

		pkgPath := path.Join(tmpDirPath, pkg)
		cmd = exec.Command("chroot", mountPath, "dpkg", "--unpack", pkgPath)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to unpack package: %w: %s", err, output)
		}
	}

	return nil
}

//go:embed grub.cfg.tmpl
var grubConfigTemplate string

func configureGrub(mountPath, kernel, initrd, cmdline string) error {
	config := struct {
		Kernel  string
		Initrd  string
		Cmdline string
	}{
		Kernel:  kernel,
		Initrd:  initrd,
		Cmdline: cmdline,
	}

	tmpl, err := template.New("grub_config").Parse(grubConfigTemplate)
	if err != nil {
		return err
	}

	grubConfigPath := path.Join(mountPath, "/boot/grub/grub.cfg")
	grubConfigFile, err := os.Create(grubConfigPath)
	if err != nil {
		return fmt.Errorf("failed to open grub config: %w", err)
	}
	defer grubConfigFile.Close()

	err = tmpl.Execute(grubConfigFile, config)
	if err != nil {
		return fmt.Errorf("failed to write grub config: %w", err)
	}

	return nil
}

func buildInitrd(mountPath, kernelVersion string) error {
	cmd := exec.Command("chroot", mountPath, "update-initramfs", "-c", "-k", kernelVersion)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update-initramfs failed: %w: %s", err, output)
	}

	return nil
}

func setupBoot(mountPath, kernelVersion string) error {
	err := buildInitrd(mountPath, kernelVersion)
	if err != nil {
		return fmt.Errorf("failed to build initrd: %w", err)
	}

	cmdline := "root=LABEL=cloudimg-rootfs ro console=tty1 console=ttyS0"
	if runtime.GOARCH == "arm64" {
		cmdline = "root=LABEL=cloudimg-rootfs ro"
	}
	kernel := fmt.Sprintf("vmlinuz-%s", kernelVersion)
	initrd := fmt.Sprintf("initrd.img-%s", kernelVersion)

	err = configureGrub(mountPath, kernel, initrd, cmdline)
	if err != nil {
		return fmt.Errorf("failed to configure grub: %w", err)
	}

	return nil
}

func customizeMount(mountPath, cloudInitConfigPath string, extraPackages []string, kernelVersion string) error {
	err := configureCloudInit(mountPath, cloudInitConfigPath)
	if err != nil {
		return fmt.Errorf("failed to configure cloud-init: %w", err)
	}

	err = installExtraPackages(mountPath, extraPackages)
	if err != nil {
		return fmt.Errorf("failed to install extra packages: %w", err)
	}

	if kernelVersion == "" {
		return nil
	}

	err = setupBoot(mountPath, kernelVersion)
	if err != nil {
		return fmt.Errorf("failed to setup boot: %w", err)
	}

	return nil
}

// isRunningAsRoot checks if the program is running as root by checking the effective UID
func isRunningAsRoot() bool {
	// Using syscall to get the effective user ID (UID)
	return syscall.Geteuid() == 0
}

var (
	configPath = flag.String("config", "", "Path to config file")
)

type Config struct {
	CloudInitConfigPath string   `json:"cloudinit_config_path"`
	ImageURL            string   `json:"image_url"`
	ExtraPackages       []string `json:"extra_packages"`
	KernelVersion       string   `json:"kernel_version"`
}

func ParseConfig(configPath string) (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}

	config := new(Config)
	err = json.NewDecoder(file).Decode(config)
	if err != nil {
		return config, err
	}

	if config.ImageURL == "" {
		return config, errors.New("image_url missing")
	}

	if config.CloudInitConfigPath == "" {
		return config, errors.New("cloudinit_config_file missing")
	}

	return config, nil
}

func mainWithExitCode() int {
	qcow2ImagePath := "ubuntu.qcow2.img"
	rawImagePath := "ubuntu.img"

	if !isRunningAsRoot() {
		slog.Error("this program needs to run as root")
		return 1
	}

	flag.Parse()
	if *configPath == "" {
		flag.Usage()
		return 2
	}
	logger := slog.Default()

	config, err := ParseConfig(*configPath)
	if err != nil {
		slog.Error("failed to parse config file", "error", err)
		return 3
	}

	if _, err := os.Stat(qcow2ImagePath); os.IsNotExist(err) {
		logger.Info(fmt.Sprintf("downloading file from %s", config.ImageURL))
		err := downloadFile(config.ImageURL, qcow2ImagePath)
		if err != nil {
			logger.Error("download failed", "error", err)
			return 4
		}
		logger.Info("download succeeded")
	} else {
		logger.Info("file found, skip download")
	}

	err = convertQCOW2ToRaw(qcow2ImagePath, rawImagePath)
	if err != nil {
		logger.Error("failed to convert to raw image failed", "error", err)
		return 5
	}
	logger = logger.With("image", rawImagePath)
	logger.Info("image converted to raw")

	loopDevice, err := attachLoopDevice(rawImagePath)
	if err != nil {
		logger.Error("failed to attach loop device", "error", err)
		return 6
	}
	defer detachLoopDevice(loopDevice)
	logger = logger.With("device", loopDevice)
	logger.Info("image attached to loop device")

	mountPath, err := os.MkdirTemp("", "mount*")
	if err != nil {
		logger.Error("failed to create mount directory", "error", err)
		return 7
	}
	defer os.RemoveAll(mountPath)
	logger = logger.With("mountPath", mountPath)

	err = mountLoopDevice(loopDevice, mountPath)
	if err != nil {
		logger.Error("failed to mount loop device", "error", err)
		return 8
	}
	defer unmountLoopDevice(mountPath)
	logger.Info("image mounted")

	err = customizeMount(mountPath, config.CloudInitConfigPath, config.ExtraPackages, config.KernelVersion)
	if err != nil {
		logger.Error("failed to modify image", "error", err)
		return 9
	}
	logger.Info("image customization done")

	return 0
}

func main() {
	os.Exit(mainWithExitCode())
}
