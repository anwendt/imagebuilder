# VM Image Builder — Claude Code Context

Dieses Dokument enthält alle Architekturentscheidungen und Designprinzipien für den
**VM Image Builder** — einen Kubernetes-nativen, deklarativen Image-Builder analog zu
HashiCorp Packer, aber vollständig unter **Apache 2.0** lizenziert.

---

## Projektziel

Kubernetes Operator der es ermöglicht, VM-Images für verschiedene Plattformen über
deklarative Kubernetes-Manifeste zu bauen. Analogie zu HashiCorp Packer, aber:
- Vollständig **Apache 2.0** lizenziert (kein BSL, kein GPL-Linking)
- Kubernetes-nativ (CRDs, Operator-Pattern, Reconciliation Loop)
- Erweiterbar über **Plugin-System** für Plattformen und Provisioner

---

## Lizenz-Constraints — KRITISCH

**Packer ist BSL 1.1 seit 2023 — darf NICHT verwendet werden.**

Erlaubte Abhängigkeiten:
| Komponente | Lizenz | Verwendung |
|---|---|---|
| controller-runtime / kubebuilder | Apache 2.0 | Operator-Framework |
| govmomi | Apache 2.0 | vSphere/VCF SDK |
| gophercloud | Apache 2.0 | OpenStack SDK |
| aws-sdk-go-v2 | Apache 2.0 | AWS SDK |
| azure-sdk-go | MIT | Azure SDK |
| google-cloud-go | Apache 2.0 | GCP SDK |
| QEMU (Userspace) | Apache 2.0 | Build-Backend |
| diskimage-builder | Apache 2.0 | Build-Backend (OpenStack) |
| go-libvirt | Apache 2.0 | libvirt Bindings via Socket |

**LGPL-Regel**: libvirt und libguestfs sind LGPL — sie werden ausschließlich als
externe Prozesse über Unix-Sockets angesprochen, **nie statisch gelinkt**.
Das hält das Projekt Apache-2.0-sauber.

---

## Unterstützte Zielplattformen

- vSphere (inkl. VMware Cloud Foundation)
- OpenStack
- AWS (AMI)
- Azure (Managed Image / Compute Gallery)
- GCP (Custom Image)

## Unterstützte Betriebssysteme

- Linux: Ubuntu, Debian, RHEL/CentOS, Rocky, AlmaLinux, Fedora, SLES
- Windows: Server 2019, 2022, 2025; Windows 10/11

---

## Architektur-Übersicht

```
VMImage Manifest (YAML)
    ↓
Kubernetes API (CRD Validierung)
    ↓
Operator Controller (Reconciliation Loop)
    ↓
Build Engine (Kubernetes Job)
    ├── QEMU/libvirt Backend      → vSphere, VCF, lokal
    ├── diskimage-builder Backend → OpenStack
    └── Cloud-API Backend         → AWS, Azure, GCP (direkt über SDK)
    ↓
Provisioner (sequenziell, Init-Container)
    ├── cloud-init, Shell, File, PowerShell (In-Process)
    └── Ansible, Chef, Custom (Init-Container / OCI Image)
    ↓
Platform Provider (Pod, gRPC)
    ├── provider-vsphere
    ├── provider-openstack
    ├── provider-aws
    ├── provider-azure
    ├── provider-gcp
    └── provider-* (Community)
```

---

## Plugin-System — zwei unabhängige Ebenen

### Ebene 1: Platform Provider (analog Crossplane)

Platform Provider sind **separate Container** die dynamisch über eine `PlatformProvider`-CRD
nachgeladen werden. Der Core-Operator startet sie als Kubernetes-Deployments und kommuniziert
über **gRPC auf Unix-Sockets**.

**Kernprinzip**: Ein neuer Provider braucht keinen Fork, keinen Core-Patch — nur ein
OCI-Image das das Protobuf-Interface implementiert.

```
PlatformProvider CR → Core Operator → Deployment starten → gRPC Handshake → Registry
```

Jeder Provider implementiert:
- `GetCapabilities()` — Name, Version, unterstützte Formate und OS-Familien
- `ValidateConfig()` — Credentials und Endpunkt prüfen
- `UploadArtifact()` — Streaming Upload des Build-Artifacts
- `RegisterImage()` — Als Platform-Image registrieren (AMI, Template, UUID...)
- `DeleteArtifact()` — Cleanup bei Fehler
- `HealthCheck()` — Liveness

**Datei**: `api/provider/v1/provider.proto` — das ist der stabile Vertrag, nie breaking ändern.

### Ebene 2: Provisioner (drei Stufen)

| Stufe | Mechanismus | Verwendung |
|---|---|---|
| In-Process | Go Interface, compile-time | cloud-init, Shell, File, PowerShell, Sysprep |
| Init-Container | OCI Image, dynamisch | Ansible, Chef, Puppet, SaltStack, Custom |
| Sidecar | Parallel zum Build | Vault Agent, SSH-Proxy (nur wenn nötig) |

**Init-Container Vertrag** (kein SDK nötig):
```
/workspace/config.json  ← Operator schreibt (VM-Adresse, SSH-Key, Spec)
/workspace/status.json  → Provisioner schreibt (success/error)
Exit 0                  → Erfolg, nächster Init-Container startet
Exit != 0               → Build schlägt fehl
```

Init-Container laufen **sequenziell** — das ist exakt die Semantik von Provisionern.

---

## CRD-Struktur

### VMImage (Haupt-Ressource)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
metadata:
  name: ubuntu-24-04-hardened
spec:
  os:
    family: linux
    distribution: ubuntu
    version: "24.04"
    arch: amd64
  source:
    type: cloud-image   # iso | cloud-image | marketplace
    url: https://...
    checksum: sha256:...
  provisioners:
    - type: cloud-init
      inline: |
        packages: [nginx]
    - type: ansible
      image: ghcr.io/yourorg/provisioner-ansible:v2.16  # optional override
      playbook: s3://bucket/harden.yml
    - type: custom
      image: ghcr.io/mycompany/provisioner-inspec:v1.0
      args: ["--profile", "cis-ubuntu-22"]
  targets:
    - providerConfigRef:
        name: aws-eu-west-1
      format: ami
    - providerConfigRef:
        name: vsphere-prod
      format: ova
  build:
    timeout: 2h
    nodeSelector:
      kubernetes.io/os: linux
status:
  phase: Building | Uploading | Ready | Failed
  images:
    - provider: aws
      imageRef: ami-0abc123
      location: eu-west-1
  conditions: [...]
```

### PlatformProvider (Provider installieren)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: PlatformProvider
metadata:
  name: provider-aws
spec:
  package: ghcr.io/yourorg/imagebuilder-provider-aws:v1.2.0
  packagePullPolicy: IfNotPresent
```

### ProviderConfig (Credentials pro Instanz)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
metadata:
  name: aws-eu-west-1
spec:
  provider: aws
  credentials:
    secretRef:
      name: aws-credentials
      key: credentials
  region: eu-west-1
```

---

## Go-Konventionen in diesem Projekt

- **Go Version**: 1.22+
- **Module**: `github.com/yourorg/imagebuilder`
- **Fehlerbehandlung**: immer `fmt.Errorf("kontext: %w", err)`, nie panic in Produktionscode
- **Logging**: `log/slog` (stdlib), strukturiert mit `slog.With()`
- **Kontext**: Jede Funktion die I/O macht bekommt `ctx context.Context` als ersten Parameter
- **Interfaces**: Klein halten — max 5-7 Methoden pro Interface (Go-Idiom)
- **Tests**: Table-driven Tests mit `t.Run()`, Mocks über Interface-Implementierungen
- **Generated Code**: Nie manuell editieren — Kommentar `// Code generated ... DO NOT EDIT.`

---

## Verzeichnisstruktur

```
imagebuilder/
├── CLAUDE.md                          ← diese Datei
├── LICENSE                            ← Apache 2.0
├── NOTICE                             ← Drittkomponenten (go-licenses generiert)
├── go.mod
├── go.sum
│
├── api/
│   ├── v1alpha1/                      ← CRD Go-Types (kubebuilder generiert)
│   │   ├── vmimage_types.go
│   │   ├── platformprovider_types.go
│   │   ├── providerconfig_types.go
│   │   └── zz_generated.deepcopy.go  ← generiert, nie anfassen
│   └── provider/v1/
│       └── provider.proto             ← gRPC Interface für Provider
│
├── pkg/
│   ├── plugin/
│   │   ├── platform/
│   │   │   └── interface.go           ← Plugin-Interface (stabil, nie breaking ändern)
│   │   ├── registry.go                ← Laufzeit-Registry aktiver Provider
│   │   └── grpc/
│   │       └── adapter.go             ← gRPC → Plugin-Interface Adapter
│   │
│   ├── provisioner/
│   │   ├── interface.go               ← Provisioner-Interface
│   │   ├── cloudinit/
│   │   ├── shell/
│   │   ├── file/
│   │   └── powershell/
│   │
│   ├── builder/
│   │   ├── interface.go               ← Builder-Interface
│   │   ├── qemu/                      ← QEMU/libvirt Backend
│   │   └── diskimage/                 ← diskimage-builder Backend
│   │
│   └── controller/
│       ├── vmimage/                   ← VMImage Reconciler
│       ├── provider/                  ← PlatformProvider Package-Controller
│       └── buildpod/                  ← Pod-Assembler (Init-Container Logik)
│
├── plugins/                           ← Built-in Platform Provider (compile-time)
│   ├── vsphere/
│   ├── openstack/
│   ├── aws/
│   ├── azure/
│   └── gcp/
│
├── cmd/
│   └── operator/
│       └── main.go                    ← Einstiegspunkt, Plugin-Imports
│
└── config/
    ├── crd/                           ← generierte CRD YAMLs
    ├── rbac/                          ← ClusterRole, ServiceAccount
    └── samples/                       ← Beispiel-Manifeste
```

---

## Wichtige Designentscheidungen (ADRs)

### ADR-001: Kein Packer
**Entscheidung**: Packer wird nicht verwendet.
**Begründung**: BSL 1.1 seit 2023, nicht Apache-2.0-kompatibel, nicht redistributierbar.
**Alternative**: Eigene Build-Engine mit QEMU/libvirt + diskimage-builder + direkte Cloud-APIs.

### ADR-002: Provider als separate Container (Crossplane-Modell)
**Entscheidung**: Platform Provider laufen als eigene Kubernetes-Pods, nicht als Go-Plugins (.so).
**Begründung**: Go's plugin-Mechanismus (.so) ist unbrauchbar (gleiche Go-Version, kein Windows,
kein Cross-Compile). Separate Container ermöglichen unabhängige Versionierung, beliebige Sprachen,
und saubere Lizenztrennung (ein proprietärer Provider kontaminiert nicht das Core-Projekt).
**Kommunikation**: gRPC über Unix-Socket (nicht TCP — kein Netzwerk-Overhead im selben Pod).

### ADR-003: Provisioner als Init-Container
**Entscheidung**: Komplexe Provisioner (Ansible, Chef) laufen als Kubernetes Init-Container.
**Begründung**: Init-Container laufen sequenziell — das entspricht exakt der Provisioner-Semantik.
Kein gRPC-Overhead nötig, Vertrag ist simpel (config.json/status.json + Exit-Code).
Community-Provisioner brauchen kein SDK, nur ein OCI-Image das den Dateipfad-Vertrag einhält.

### ADR-004: LGPL-Abhängigkeiten nur als externe Prozesse
**Entscheidung**: libvirt und libguestfs werden nur über CLI/Socket angesprochen.
**Begründung**: Statisches oder dynamisches Linken gegen LGPL würde Apache-2.0-Redistribution
einschränken. Prozess-Kommunikation ist lizenzrechtlich unbedenklich.

### ADR-005: Protobuf-Schema ist versionierter Vertrag
**Entscheidung**: `api/provider/v1/provider.proto` ist das stabile Interface.
**Konsequenz**: Breaking Changes → `api/provider/v2/`. Nie Felder aus v1 entfernen.
Field-Numbers in Proto sind unveränderlich.

### ADR-006: Kein Go plugin-Mechanismus
**Entscheidung**: Keine .so-Dateien, keine Go plugin-Package-Nutzung.
**Alternative für compile-time Plugins**: `init()`-Pattern mit Blank-Import in main.go
(analog zu database/sql Treibern). Für Runtime-Plugins: gRPC-Container.

---

## Build & Entwicklung

```bash
# CRDs generieren (nach Änderungen an api/v1alpha1/)
make generate
make manifests

# Operator lokal starten (gegen aktuellen kubeconfig-Kontext)
make run

# Tests
make test

# Lizenz-Check (vor jedem Release)
go install github.com/google/go-licenses@latest
go-licenses check ./...

# Provider-Image bauen
docker build -t ghcr.io/yourorg/imagebuilder-provider-aws:dev ./plugins/aws/

# NOTICE-Datei aktualisieren
go-licenses report ./... > NOTICE
```

---

## Noch nicht entschieden / TODO

- [ ] Image-Caching-Strategie (Basis-ISOs in PVC oder S3-kompatibler Store)
- [ ] Parallelisierung von Builds (max. concurrent builds per Node)
- [ ] Webhook-Validierung für VMImage-Spec (kubebuilder Validating Webhook)
- [ ] OCI-Signierung der Provider-Images (cosign / Sigstore)
- [ ] Metrics (Prometheus) — Build-Dauer, Fehlerrate, Provider-Latenz
- [ ] Multi-Arch Support (arm64)
- [ ] Windows: cloudbase-init Integration finalisieren
- [ ] Provider-SDK Repository aufsetzen
