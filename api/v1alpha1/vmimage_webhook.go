// api/v1alpha1/vmimage_webhook.go
//
// Validating Admission Webhook for VMImage (AS-026, AS-047, REQ-008).
//
// Enforces rules that cannot be expressed as kubebuilder markers alone:
//   - downloadable spec.source.url values must not resolve to a private / cloud-metadata IP (SSRF, AS-047)
//   - spec.source.checksum is required when downloadable spec.source.url is set (AS-010)
//   - spec.targets must not be empty
//
// Registration in cmd/operator/main.go (or manager setup):
//   if err := (&v1alpha1.VMImage{}).SetupWebhookWithManager(mgr); err != nil { ... }
//
// +kubebuilder:webhook:path=/validate-imagebuilder-io-v1alpha1-vmimage,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,groups=imagebuilder.io,resources=vmimages,verbs=create;update,versions=v1alpha1,name=vvmimage.kb.io,admissionReviewVersions=v1,timeoutSeconds=10,serviceName=imagebuilder-webhook-service,serviceNamespace=imagebuilder-system

package v1alpha1

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the VMImage validating webhook.
func (r *VMImage) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(r).Complete()
}

// Ensure VMImage implements the (deprecated-but-supported) webhook.Validator interface.
var _ webhook.Validator = &VMImage{}

// ValidateCreate validates a new VMImage.
func (r *VMImage) ValidateCreate() (admission.Warnings, error) {
	return r.validate()
}

// ValidateUpdate validates a VMImage update.
func (r *VMImage) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return r.validate()
}

// ValidateDelete is a no-op — VMImage deletions are always allowed.
func (r *VMImage) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

// validate runs all admission checks for VMImage.
func (r *VMImage) validate() (admission.Warnings, error) {
	var errs []error

	// AS-010: checksum required whenever a downloadable source URL is provided.
	if strings.HasPrefix(r.Spec.Source.URL, "https://") && r.Spec.Source.Checksum == "" {
		errs = append(errs, fmt.Errorf(
			"spec.source.checksum is required when spec.source.url is set (AS-010)"))
	}
	buildMode := r.Spec.Build.Mode
	if buildMode == "" {
		buildMode = BuildModeLocal
	}
	if err := validateOSSpec(r.Spec.OS); err != nil {
		errs = append(errs, err)
	}
	switch buildMode {
	case BuildModeLocal, BuildModeRemote:
	default:
		errs = append(errs, fmt.Errorf("spec.build.mode must be local or remote"))
	}

	if strings.HasPrefix(r.Spec.Source.URL, "https://") {
		// AS-047: SSRF check — source URL must not resolve to a private IP.
		if err := validateNoSSRF("spec.source.url", r.Spec.Source.URL); err != nil {
			errs = append(errs, err)
		}
	} else if r.Spec.Source.URL != "" {
		errs = append(errs, fmt.Errorf("spec.source.url must be an https URL; use spec.source.providerRef for provider-native source references"))
	}
	if r.Spec.Source.ProviderRef != "" && strings.HasPrefix(r.Spec.Source.ProviderRef, "http://") {
		errs = append(errs, fmt.Errorf("spec.source.providerRef must be a provider-native source reference, not an insecure URL"))
	}
	if err := validateSourceCache(r.Spec.Build); err != nil {
		errs = append(errs, err)
	}

	// boot_command is only meaningful when booting from an ISO. Reject it for
	// cloud-image and marketplace sources where the VM never shows a boot menu.
	if len(r.Spec.Source.BootCommand) > 0 && r.Spec.Source.Type != "iso" {
		errs = append(errs, fmt.Errorf(
			"spec.source.bootCommand is only supported for source type \"iso\", got %q",
			r.Spec.Source.Type))
	}
	if r.Spec.Source.Installer != nil {
		if r.Spec.Source.Type != "iso" {
			errs = append(errs, fmt.Errorf(
				"spec.source.installer is only supported for source type \"iso\", got %q",
				r.Spec.Source.Type))
		}
		if err := validateInstallerMedia(r.Spec.OS, r.Spec.Source.Installer); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Spec.Build.GuestAccess != nil {
		if buildMode == BuildModeLocal && r.Spec.Source.Type != "iso" {
			errs = append(errs, fmt.Errorf(
				"spec.build.guestAccess is only supported for source type \"iso\", got %q",
				r.Spec.Source.Type))
		}
		if err := validateGuestAccess(r.Spec.OS, r.Spec.Build.GuestAccess, buildMode); err != nil {
			errs = append(errs, err)
		}
	}
	if err := validateProvisionerImagePolicy(r.Spec.Provisioners, r.Spec.Build.Security); err != nil {
		errs = append(errs, err)
	}

	// Structural: at least one upload target must be specified.
	if len(r.Spec.Targets) == 0 {
		errs = append(errs, fmt.Errorf("spec.targets must contain at least one entry"))
	}
	if buildMode == BuildModeRemote && len(r.Spec.Targets) != 1 {
		errs = append(errs, fmt.Errorf("spec.build.mode remote currently requires exactly one target"))
	}

	if len(errs) > 0 {
		return nil, joinErrors(errs)
	}
	return nil, nil
}

func validateOSSpec(os OSSpec) error {
	switch os.Arch {
	case "", "amd64", "arm64":
		return nil
	default:
		return fmt.Errorf("spec.os.arch must be amd64 or arm64")
	}
}

func validateSourceCache(build BuildSpec) error {
	if build.Cache == nil {
		return nil
	}
	legacyRef := ""
	if build.CacheRef != nil {
		legacyRef = *build.CacheRef
	}
	if legacyRef != "" && build.Cache.Ref != "" && legacyRef != build.Cache.Ref {
		return fmt.Errorf("spec.build.cacheRef and spec.build.cache.ref must reference the same PVC when both are set")
	}
	effectiveRef := build.Cache.Ref
	if effectiveRef == "" {
		effectiveRef = legacyRef
	}
	if effectiveRef == "" {
		return fmt.Errorf("spec.build.cache.ref is required when spec.build.cache is set")
	}
	if build.Cache.TTL != nil && build.Cache.TTL.Duration <= 0 {
		return fmt.Errorf("spec.build.cache.ttl must be greater than zero")
	}
	switch build.Cache.RetainPolicy {
	case "", "Always", "Never":
		return nil
	default:
		return fmt.Errorf("spec.build.cache.retainPolicy must be Always or Never")
	}
}

func validateProvisionerImagePolicy(provisioners []ProvisionerSpec, security *BuildSecuritySpec) error {
	if security == nil || security.ProvisionerImages == nil {
		return nil
	}
	for i, p := range provisioners {
		if p.Image == "" {
			continue
		}
		if err := validateImagePolicy(fmt.Sprintf("spec.provisioners[%d].image", i), p.Image, security.ProvisionerImages); err != nil {
			return err
		}
	}
	return nil
}

func validateImagePolicy(fieldPath, image string, policy *ImagePolicySpec) error {
	if policy == nil || image == "" {
		return nil
	}
	if policy.RequireDigest || policy.VerifySignature {
		if !strings.Contains(image, "@sha256:") {
			return fmt.Errorf("%s must be pinned by digest when requireDigest or verifySignature is enabled", fieldPath)
		}
	}
	if len(policy.AllowedRegistries) > 0 {
		for _, prefix := range policy.AllowedRegistries {
			prefix = strings.TrimSuffix(prefix, "/")
			if image == prefix || strings.HasPrefix(image, prefix+"/") {
				return nil
			}
		}
		return fmt.Errorf("%s registry is not in spec.build.security.provisionerImages.allowedRegistries", fieldPath)
	}
	return nil
}

func validateInstallerMedia(os OSSpec, installer *InstallerMediaSpec) error {
	switch installer.Type {
	case "nocloud", "autoinstall":
		if os.Family != "linux" {
			return fmt.Errorf("spec.source.installer.type %q requires spec.os.family linux", installer.Type)
		}
		if installer.UserData == "" {
			return fmt.Errorf("spec.source.installer.userData is required for installer type %q", installer.Type)
		}
	case "kickstart":
		if os.Family != "linux" {
			return fmt.Errorf("spec.source.installer.type kickstart requires spec.os.family linux")
		}
		if installer.Kickstart == "" {
			return fmt.Errorf("spec.source.installer.kickstart is required for installer type \"kickstart\"")
		}
	case "preseed":
		if os.Family != "linux" {
			return fmt.Errorf("spec.source.installer.type preseed requires spec.os.family linux")
		}
		if installer.Preseed == "" {
			return fmt.Errorf("spec.source.installer.preseed is required for installer type \"preseed\"")
		}
	case "autounattend":
		if os.Family != "windows" {
			return fmt.Errorf("spec.source.installer.type autounattend requires spec.os.family windows")
		}
		if installer.Autounattend == "" && installer.Windows == nil {
			return fmt.Errorf("spec.source.installer.autounattend or spec.source.installer.windows is required for installer type \"autounattend\"")
		}
		if installer.Windows != nil && installer.Windows.CloudbaseInit != nil &&
			strings.TrimSpace(installer.Windows.CloudbaseInitMSI) == "" {
			return fmt.Errorf("spec.source.installer.windows.cloudbaseInit requires spec.source.installer.windows.cloudbaseInitMsi")
		}
	default:
		return fmt.Errorf("spec.source.installer.type must be nocloud, autoinstall, kickstart, preseed, or autounattend")
	}
	return nil
}

func validateGuestAccess(os OSSpec, access *GuestAccessSpec, buildMode string) error {
	if access.Protocol != "ssh" && access.Protocol != "winrm" {
		return fmt.Errorf("spec.build.guestAccess.protocol must be ssh or winrm")
	}
	switch os.Family {
	case "linux":
		if access.Protocol != "ssh" {
			return fmt.Errorf("spec.build.guestAccess.protocol must be ssh when spec.os.family is linux")
		}
	case "windows":
		if access.Protocol != "winrm" {
			return fmt.Errorf("spec.build.guestAccess.protocol must be winrm when spec.os.family is windows")
		}
	}
	if buildMode == BuildModeLocal && access.Host != "" && access.Host != "127.0.0.1" && access.Host != "localhost" {
		return fmt.Errorf("spec.build.guestAccess.host must be 127.0.0.1 or localhost for qemu user networking")
	}
	if access.HostPort < 1 || access.HostPort > 65535 {
		return fmt.Errorf("spec.build.guestAccess.hostPort must be between 1 and 65535")
	}
	if access.GuestPort < 0 || access.GuestPort > 65535 {
		return fmt.Errorf("spec.build.guestAccess.guestPort must be between 1 and 65535 when set")
	}
	if access.Timeout != nil && access.Timeout.Duration <= 0 {
		return fmt.Errorf("spec.build.guestAccess.timeout must be greater than zero")
	}
	if access.Credentials != nil {
		creds := access.Credentials
		if creds.SecretRef != nil && creds.SecretRef.Name == "" {
			return fmt.Errorf("spec.build.guestAccess.credentials.secretRef.name is required")
		}
		if creds.SecretRef != nil && creds.Generate != nil {
			return fmt.Errorf("spec.build.guestAccess.credentials.secretRef and generate are mutually exclusive")
		}
		if creds.Generate != nil {
			if !creds.Generate.SSHKey && !creds.Generate.Password {
				return fmt.Errorf("spec.build.guestAccess.credentials.generate must enable sshKey or password")
			}
			if creds.Generate.PasswordLength != 0 &&
				(creds.Generate.PasswordLength < 20 || creds.Generate.PasswordLength > 128) {
				return fmt.Errorf("spec.build.guestAccess.credentials.generate.passwordLength must be between 20 and 128")
			}
		}
		if creds.Injection != nil {
			switch creds.Injection.Method {
			case "", "none", "cloud-init", "autounattend":
			default:
				return fmt.Errorf("spec.build.guestAccess.credentials.injection.method must be none, cloud-init, or autounattend")
			}
			switch os.Family {
			case "linux":
				if creds.Injection.Method == "autounattend" {
					return fmt.Errorf("spec.build.guestAccess.credentials.injection.method autounattend requires spec.os.family windows")
				}
			case "windows":
				if creds.Injection.Method == "cloud-init" {
					return fmt.Errorf("spec.build.guestAccess.credentials.injection.method cloud-init requires spec.os.family linux")
				}
			}
		}
	}
	if access.WinRM != nil && access.WinRM.InsecureSkipVerify {
		if access.Protocol != "winrm" {
			return fmt.Errorf("spec.build.guestAccess.winrm.insecureSkipVerify is only valid with protocol winrm")
		}
		if access.WinRM.HTTPS != nil && !*access.WinRM.HTTPS {
			return fmt.Errorf("spec.build.guestAccess.winrm.insecureSkipVerify requires winrm.https true")
		}
		if access.Credentials == nil || access.Credentials.Generate == nil || !access.Credentials.Generate.Password {
			return fmt.Errorf("spec.build.guestAccess.winrm.insecureSkipVerify is only allowed for generated ephemeral WinRM bootstrap credentials")
		}
	}
	return nil
}

// joinErrors merges multiple errors into one admission-response-friendly string.
func joinErrors(errs []error) error {
	msg := ""
	for i, e := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += e.Error()
	}
	return fmt.Errorf("%s", msg) //nolint:goerr113
}
