package builder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

const (
	guestCredentialsDirName = "guest-credentials" // #nosec G101 -- Directory name for generated credentials, not credential material.
	guestSSHPrivateKeyName  = "id_ed25519"
	guestSSHPublicKeyName   = "id_ed25519.pub"
	guestPasswordName       = "password"
	defaultGeneratedUser    = "imagebuilder"
	defaultPasswordLength   = int32(32)
)

// GeneratedGuestCredentials describes temporary credentials generated for a
// single build. Values are kept out of Kubernetes env vars and status fields.
type GeneratedGuestCredentials struct {
	Dir            string
	PrivateKeyPath string
	PublicKey      string
	PasswordPath   string
	Password       string
}

func prepareGuestCredentials(ctx context.Context, img *v1alpha1.VMImage, workspaceDir, credentialDir string) (GeneratedGuestCredentials, error) {
	_ = ctx
	if img == nil || img.Spec.Build.GuestAccess == nil ||
		img.Spec.Build.GuestAccess.Credentials == nil ||
		img.Spec.Build.GuestAccess.Credentials.Generate == nil {
		return GeneratedGuestCredentials{}, nil
	}
	access := img.Spec.Build.GuestAccess
	if access.User == "" {
		access.User = defaultGuestUser(img)
	}
	if credentialDir == "" {
		credentialDir = filepath.Join(workspaceDir, guestCredentialsDirName)
	}
	credsDir := filepath.Join(credentialDir, guestCredentialsDirName)
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		return GeneratedGuestCredentials{}, fmt.Errorf("create guest credentials directory: %w", err)
	}

	generated := GeneratedGuestCredentials{Dir: credsDir}
	spec := access.Credentials.Generate
	if spec.SSHKey {
		privatePath := filepath.Join(credsDir, guestSSHPrivateKeyName)
		publicKey, err := writeEphemeralSSHKey(privatePath)
		if err != nil {
			return GeneratedGuestCredentials{}, err
		}
		generated.PrivateKeyPath = privatePath
		generated.PublicKey = publicKey
		access.SSHKeyPath = privatePath
	}
	if spec.Password {
		length := spec.PasswordLength
		if length == 0 {
			length = defaultPasswordLength
		}
		password, err := randomPassword(length)
		if err != nil {
			return GeneratedGuestCredentials{}, err
		}
		passwordPath := filepath.Join(credsDir, guestPasswordName)
		if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0o600); err != nil {
			return GeneratedGuestCredentials{}, fmt.Errorf("write generated guest password: %w", err)
		}
		generated.Password = password
		generated.PasswordPath = passwordPath
		access.PasswordPath = passwordPath
	}
	if err := injectGeneratedCredentials(img, workspaceDir, generated); err != nil {
		return GeneratedGuestCredentials{}, err
	}
	return generated, nil
}

func cleanupGeneratedGuestCredentials(workspaceDir string, creds GeneratedGuestCredentials, installDataISOs []string) {
	if creds.Dir != "" {
		_ = os.RemoveAll(creds.Dir)
	}
	for _, path := range installDataISOs {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	_ = os.RemoveAll(filepath.Join(workspaceDir, autounattendSeedDirName))
	_ = os.RemoveAll(filepath.Join(workspaceDir, cloudInitSeedDirName))
}

func writeEphemeralSSHKey(path string) (string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ephemeral ssh key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("marshal ephemeral ssh key: %w", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", fmt.Errorf("write ephemeral ssh key: %w", err)
	}
	authorizedKey := authorizedKeysEd25519(publicKey)
	if err := os.WriteFile(path+".pub", []byte(authorizedKey+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write ephemeral ssh public key: %w", err)
	}
	return authorizedKey, nil
}

func authorizedKeysEd25519(publicKey ed25519.PublicKey) string {
	const algo = "ssh-ed25519"
	var wire []byte
	wire = appendSSHString(wire, algo)
	wire = appendSSHString(wire, string(publicKey))
	return algo + " " + base64.StdEncoding.EncodeToString(wire)
}

func appendSSHString(buf []byte, value string) []byte {
	if len(value) > math.MaxUint32 {
		panic("ssh wire string exceeds uint32 length")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value))) // #nosec G115 -- Length is bounded above before conversion to the SSH wire uint32 length field.
	buf = append(buf, length[:]...)
	return append(buf, value...)
}

func randomPassword(length int32) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_"
	if length < 20 || length > 128 {
		return "", fmt.Errorf("generated guest password length must be between 20 and 128")
	}
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate guest password: %w", err)
	}
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw), nil
}

func injectGeneratedCredentials(img *v1alpha1.VMImage, workspaceDir string, creds GeneratedGuestCredentials) error {
	if creds.PublicKey == "" && creds.Password == "" {
		return nil
	}
	method := credentialInjectionMethod(img)
	switch method {
	case "none":
		return nil
	case "cloud-init":
		ensureCloudInitCredentials(img, creds)
		return nil
	case "autounattend":
		return writeAutounattendCredentials(img, workspaceDir, creds)
	default:
		return fmt.Errorf("unsupported guest credential injection method %q", method)
	}
}

func credentialInjectionMethod(img *v1alpha1.VMImage) string {
	creds := img.Spec.Build.GuestAccess.Credentials
	if creds.Injection != nil && creds.Injection.Method != "" {
		return creds.Injection.Method
	}
	if strings.EqualFold(img.Spec.OS.Family, "windows") {
		return "autounattend"
	}
	return "cloud-init"
}

func ensureCloudInitCredentials(img *v1alpha1.VMImage, creds GeneratedGuestCredentials) {
	snippet := cloudInitCredentialSnippet(img.Spec.Build.GuestAccess.User, creds)
	for i := range img.Spec.Provisioners {
		if img.Spec.Provisioners[i].Type == "cloud-init" {
			img.Spec.Provisioners[i].Inline = mergeCloudInit(img.Spec.Provisioners[i].Inline, snippet)
			return
		}
	}
	img.Spec.Provisioners = append([]v1alpha1.ProvisionerSpec{{
		Type:   "cloud-init",
		Inline: snippet,
	}}, img.Spec.Provisioners...)
}

func cloudInitCredentialSnippet(user string, creds GeneratedGuestCredentials) string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	b.WriteString("users:\n")
	b.WriteString("  - default\n")
	b.WriteString("  - name: " + user + "\n")
	b.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	b.WriteString("    groups: [adm, sudo]\n")
	b.WriteString("    shell: /bin/bash\n")
	if creds.PublicKey != "" {
		b.WriteString("    ssh_authorized_keys:\n")
		b.WriteString("      - " + creds.PublicKey + "\n")
	}
	if creds.Password != "" {
		b.WriteString("    lock_passwd: false\n")
		b.WriteString("chpasswd:\n")
		b.WriteString("  expire: false\n")
		b.WriteString("  users:\n")
		b.WriteString("    - name: " + user + "\n")
		b.WriteString("      password: " + creds.Password + "\n")
		b.WriteString("      type: text\n")
	}
	return b.String()
}

func mergeCloudInit(existing, generated string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return generated
	}
	generated = strings.TrimPrefix(generated, "#cloud-config\n")
	if strings.HasPrefix(existing, "#cloud-config") {
		return existing + "\n" + generated
	}
	return "#cloud-config\n" + existing + "\n" + generated
}

func defaultGuestUser(img *v1alpha1.VMImage) string {
	if strings.EqualFold(img.Spec.OS.Family, "windows") {
		return "Administrator"
	}
	return defaultGeneratedUser
}

func writeAutounattendCredentials(img *v1alpha1.VMImage, workspaceDir string, creds GeneratedGuestCredentials) error {
	if creds.Password == "" {
		return fmt.Errorf("autounattend credential injection requires generated password credentials")
	}
	dir := filepath.Join(workspaceDir, autounattendSeedDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create autounattend directory: %w", err)
	}
	var windows *v1alpha1.WindowsInstallerMediaSpec
	if img.Spec.Source.Installer != nil {
		windows = img.Spec.Source.Installer.Windows
		if strings.TrimSpace(img.Spec.Source.Installer.Autounattend) != "" {
			if _, err := os.Stat(filepath.Join(dir, autounattendConfigName)); err == nil {
				return fmt.Errorf("generated WinRM credentials cannot be merged into custom spec.source.installer.autounattend; use generated autounattend with spec.source.installer.windows hooks or provide credentials manually")
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat custom autounattend file: %w", err)
			}
		}
	}
	content := autounattendXML(img.Spec.OS.Arch, img.Spec.Build.GuestAccess.User, creds.Password, windows)
	if err := os.WriteFile(filepath.Join(dir, autounattendConfigName), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write autounattend credentials: %w", err)
	}
	return nil
}

func autounattendXML(arch, user, password string, windows *v1alpha1.WindowsInstallerMediaSpec) string {
	componentArch := windowsSetupProcessorArchitecture(arch)
	escapedUser := xmlEscape(user)
	escapedPassword := xmlEscape(password)
	driverPaths := autounattendDriverPaths(componentArch, windows)
	accountSetup := ""
	if password != "" {
		accountSetup = `
      <UserAccounts>
        <AdministratorPassword>
          <Value>` + escapedPassword + `</Value>
          <PlainText>true</PlainText>
        </AdministratorPassword>
      </UserAccounts>
      <AutoLogon>
        <Password>
          <Value>` + escapedPassword + `</Value>
          <PlainText>true</PlainText>
        </Password>
        <Enabled>true</Enabled>
        <Username>` + escapedUser + `</Username>
      </AutoLogon>`
	}
	firstLogonCommands := autounattendFirstLogonCommands(windows)
	return `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
` + driverPaths + `
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="` + componentArch + `" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
` + accountSetup + `
      <FirstLogonCommands>
` + firstLogonCommands + `
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
`
}

func windowsSetupProcessorArchitecture(arch string) string {
	if strings.EqualFold(arch, "arm64") {
		return "arm64"
	}
	return "amd64"
}

func autounattendDriverPaths(componentArch string, windows *v1alpha1.WindowsInstallerMediaSpec) string {
	if windows == nil || strings.TrimSpace(windows.VirtioDriverPath) == "" {
		return ""
	}
	return `  <settings pass="windowsPE">
    <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="` + componentArch + `" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DriverPaths>
        <PathAndCredentials wcm:action="add" wcm:keyValue="1" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
          <Path>` + xmlEscape(windows.VirtioDriverPath) + `</Path>
        </PathAndCredentials>
      </DriverPaths>
    </component>
  </settings>`
}

func autounattendFirstLogonCommands(windows *v1alpha1.WindowsInstallerMediaSpec) string {
	var b strings.Builder
	order := 1
	if windows != nil && strings.TrimSpace(windows.CloudbaseInitMSI) != "" {
		b.WriteString(autounattendCommand(order, "Install Cloudbase-Init", `msiexec.exe /i "`+windows.CloudbaseInitMSI+`" /qn /norestart`))
		order++
		for _, command := range cloudbaseInitFirstLogonCommands(windows.CloudbaseInit) {
			b.WriteString(autounattendCommand(order, command.description, command.command))
			order++
		}
	}
	b.WriteString(autounattendCommand(order, "Enable WinRM HTTPS for imagebuilder", `powershell.exe -ExecutionPolicy Bypass -Command "$cert = New-SelfSignedCertificate -DnsName $env:COMPUTERNAME -CertStoreLocation Cert:\LocalMachine\My; winrm quickconfig -q; winrm create winrm/config/Listener?Address=*+Transport=HTTPS ('@{Hostname=&quot;' + $env:COMPUTERNAME + '&quot;;CertificateThumbprint=&quot;' + $cert.Thumbprint + '&quot;}'); Set-Item WSMan:\localhost\Service\Auth\Basic $true; New-NetFirewallRule -DisplayName 'Imagebuilder WinRM HTTPS' -Direction Inbound -Protocol TCP -LocalPort 5986 -Action Allow"`))
	return b.String()
}

type firstLogonCommand struct {
	description string
	command     string
}

func cloudbaseInitFirstLogonCommands(spec *v1alpha1.CloudbaseInitSpec) []firstLogonCommand {
	return []firstLogonCommand{
		{
			description: "Configure Cloudbase-Init",
			command:     cloudbaseInitConfigCommand(spec),
		},
		{
			description: "Enable Cloudbase-Init service",
			command:     `powershell.exe -ExecutionPolicy Bypass -Command "Set-Service -Name cloudbase-init -StartupType Automatic; sc.exe failure cloudbase-init reset= 60 actions= restart/5000/restart/5000/none/5000 | Out-Null"`,
		},
	}
}

func cloudbaseInitConfigCommand(spec *v1alpha1.CloudbaseInitSpec) string {
	config := cloudbaseInitConfig(spec)
	escaped := strings.ReplaceAll(config, "`", "``")
	return `powershell.exe -ExecutionPolicy Bypass -Command "$dir = 'C:\Program Files\Cloudbase Solutions\Cloudbase-Init\conf'; New-Item -ItemType Directory -Force -Path $dir | Out-Null; @'
` + escaped + `
'@ | Set-Content -LiteralPath (Join-Path $dir 'cloudbase-init.conf') -Encoding ASCII; @'
` + escaped + `
'@ | Set-Content -LiteralPath (Join-Path $dir 'cloudbase-init-unattend.conf') -Encoding ASCII"`
}

func cloudbaseInitConfig(spec *v1alpha1.CloudbaseInitSpec) string {
	metadataServices := []string{
		"cloudbaseinit.metadata.services.configdrive.ConfigDriveService",
		"cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService",
	}
	plugins := []string{
		"cloudbaseinit.plugins.common.mtu.MTUPlugin",
		"cloudbaseinit.plugins.windows.ntpclient.NTPClientPlugin",
		"cloudbaseinit.plugins.common.sethostname.SetHostNamePlugin",
		"cloudbaseinit.plugins.windows.createuser.CreateUserPlugin",
		"cloudbaseinit.plugins.windows.networkconfig.NetworkConfigPlugin",
		"cloudbaseinit.plugins.common.setuserpassword.SetUserPasswordPlugin",
		"cloudbaseinit.plugins.common.sshpublickeys.SetUserSSHPublicKeysPlugin",
		"cloudbaseinit.plugins.common.localscripts.LocalScriptsPlugin",
	}
	localScripts := `C:\Program Files\Cloudbase Solutions\Cloudbase-Init\LocalScripts`
	groups := []string{"Administrators"}
	if spec != nil {
		if len(spec.MetadataServices) > 0 {
			metadataServices = spec.MetadataServices
		}
		if len(spec.Plugins) > 0 {
			plugins = spec.Plugins
		}
		if strings.TrimSpace(spec.LocalScriptsPath) != "" {
			localScripts = spec.LocalScriptsPath
		}
		if len(spec.AddUserToLocalGroups) > 0 {
			groups = spec.AddUserToLocalGroups
		}
	}
	return "[DEFAULT]\n" +
		"metadata_services=" + strings.Join(metadataServices, ",") + "\n" +
		"plugins=" + strings.Join(plugins, ",") + "\n" +
		"local_scripts_path=" + localScripts + "\n" +
		"groups=" + strings.Join(groups, ",") + "\n" +
		"inject_user_password=true\n" +
		"first_logon_behaviour=no\n" +
		"bsdtar_path=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\bin\\bsdtar.exe\n" +
		"mtools_path=C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\bin\n"
}

func autounattendCommand(order int, description, command string) string {
	return `        <SynchronousCommand wcm:action="add" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
          <Order>` + fmt.Sprintf("%d", order) + `</Order>
          <Description>` + xmlEscape(description) + `</Description>
          <CommandLine>` + xmlEscape(command) + `</CommandLine>
        </SynchronousCommand>
`
}

func xmlEscape(value string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(value)); err != nil {
		return value
	}
	return b.String()
}
