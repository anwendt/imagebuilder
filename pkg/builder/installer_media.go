package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

const (
	cloudInitSeedDirName    = "cloud-init"
	cloudInitUserDataName   = "user-data"
	cloudInitMetaDataName   = "meta-data"
	cloudInitNetworkName    = "network-config"
	kickstartSeedDirName    = "kickstart"
	kickstartConfigName     = "ks.cfg"
	preseedSeedDirName      = "preseed"
	preseedConfigName       = "preseed.cfg"
	autounattendSeedDirName = "autounattend"
	autounattendConfigName  = "Autounattend.xml"
)

func prepareInstallerMedia(ctx context.Context, img *v1alpha1.VMImage, workspaceDir string) error {
	_ = ctx
	if img == nil || img.Spec.Source.Installer == nil {
		return nil
	}
	installer := img.Spec.Source.Installer
	switch strings.ToLower(installer.Type) {
	case "nocloud", "autoinstall":
		return prepareCloudInitInstallerMedia(installer, workspaceDir)
	case "kickstart":
		return writeInstallerFile(filepath.Join(workspaceDir, kickstartSeedDirName), kickstartConfigName, installer.Kickstart)
	case "preseed":
		return writeInstallerFile(filepath.Join(workspaceDir, preseedSeedDirName), preseedConfigName, installer.Preseed)
	case "autounattend":
		return prepareAutounattendInstallerMedia(img, workspaceDir)
	default:
		return fmt.Errorf("unsupported installer media type %q", installer.Type)
	}
}

func prepareCloudInitInstallerMedia(installer *v1alpha1.InstallerMediaSpec, workspaceDir string) error {
	dir := filepath.Join(workspaceDir, cloudInitSeedDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cloud-init installer directory: %w", err)
	}
	userDataPath := filepath.Join(dir, cloudInitUserDataName)
	userData := installer.UserData
	if existing, err := os.ReadFile(userDataPath); err == nil && len(strings.TrimSpace(string(existing))) > 0 {
		userData = mergeCloudInit(userData, string(existing))
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing cloud-init user-data: %w", err)
	}
	if err := os.WriteFile(userDataPath, []byte(userData), 0o600); err != nil {
		return fmt.Errorf("write cloud-init installer user-data: %w", err)
	}
	metaData := installer.MetaData
	if strings.TrimSpace(metaData) == "" {
		metaData = defaultCloudInitMetaData()
	}
	if err := os.WriteFile(filepath.Join(dir, cloudInitMetaDataName), []byte(metaData), 0o600); err != nil {
		return fmt.Errorf("write cloud-init installer meta-data: %w", err)
	}
	if strings.TrimSpace(installer.NetworkConfig) != "" {
		if err := os.WriteFile(filepath.Join(dir, cloudInitNetworkName), []byte(installer.NetworkConfig), 0o600); err != nil {
			return fmt.Errorf("write cloud-init installer network-config: %w", err)
		}
	}
	return nil
}

func prepareAutounattendInstallerMedia(img *v1alpha1.VMImage, workspaceDir string) error {
	installer := img.Spec.Source.Installer
	content := installer.Autounattend
	if strings.TrimSpace(content) == "" {
		user := defaultGuestUser(img)
		if img.Spec.Build.GuestAccess != nil && img.Spec.Build.GuestAccess.User != "" {
			user = img.Spec.Build.GuestAccess.User
		}
		content = autounattendXML(img.Spec.OS.Arch, user, "", installer.Windows)
	}
	return writeInstallerFile(filepath.Join(workspaceDir, autounattendSeedDirName), autounattendConfigName, content)
}

func writeInstallerFile(dir, name, content string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create installer directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write installer file %s: %w", name, err)
	}
	return nil
}

func defaultCloudInitMetaData() string {
	return "instance-id: imagebuilder\nlocal-hostname: imagebuilder\n"
}
