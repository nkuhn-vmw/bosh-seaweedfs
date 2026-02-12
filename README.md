# SeaweedFS BOSH Release & Tanzu Tile

A BOSH release and Tanzu Operations Manager tile for [SeaweedFS](https://github.com/seaweedfs/seaweedfs), a fast distributed storage system for blobs, objects, files, and data lake storage with O(1) disk seek and cloud tiering.

Created by **Kuhn Labs**. This is an experimental, community-driven project.

## Features

- **S3-Compatible Object Storage** -- SeaweedFS master, volume, filer, and S3 gateway with full S3 API
- **Per-Binding IAM Credentials** -- Each `cf bind-service` creates isolated IAM users with bucket-scoped access keys via SeaweedFS's embedded IAM API
- **Shared Bucket Plan** -- Developers get dedicated S3 buckets on a shared cluster with synchronous provisioning
- **On-Demand Dedicated Clusters** -- Isolated SeaweedFS clusters provisioned via BOSH (single-node or HA) with async lifecycle management
- **Admin Console** -- Password-protected `weed admin` dashboard for cluster management and monitoring
- **Cloud Foundry Route Registration** -- S3, broker, and console endpoints exposed through gorouter with NATS mTLS
- **On-Demand Route Registration** -- Dedicated clusters register routes via property-based NATS config (no cross-deployment links needed)
- **CredHub Integration** -- Optional secure credential storage for service binding secrets
- **OpenTelemetry Metrics** -- Built-in otel-collector scrapes Prometheus metrics from all SeaweedFS components and exports via OTLP
- **Syslog Forwarding** -- Configurable log drain to external syslog endpoints with TLS support
- **Backup & Restore** -- Scheduled backups to local disk, NFS, or S3-compatible storage with configurable retention
- **Upgrade All Service Instances** -- Errand to roll updates across all on-demand dedicated clusters
- **Smoke Tests** -- Automated errand validates end-to-end S3 operations (put, get, list, delete) for both shared and on-demand plans
- **CI/CD Pipeline** -- GitHub Actions watches upstream SeaweedFS releases, builds BOSH release + tile, and publishes GitHub Releases
- **Tanzu Operations Manager Tile** -- One-click deployment with operator-configurable forms

## Architecture

### Shared Cluster

The tile deploys a shared SeaweedFS cluster with an S3-compatible API, service broker, and admin console. All external traffic routes through the Cloud Foundry gorouter with TLS termination.

```
                         Developer
                            |
                       cf create-service
                            |
                            v
                    +------------------+
                    | Cloud Controller |
                    +--------+---------+
                            |
                            v
   +----------------------------------------------------+
   |                    GoRouter                         |
   |               (TLS Termination)                     |
   +------+----------------+----------------+-----------+
          |                |                |
   broker.sys.dom   s3.sys.dom    admin.sys.dom
          |                |                |
          v                v                v
   +-----------+    +------------+   +------------+
   |  Broker   |    | S3 Gateway |   |   Admin    |
   |  (OSB)    |    |  + IAM API |   |  Console   |
   +-----+-----+    +-----+------+   +------------+
         |                |
    Create Bucket    S3 API Requests
    Manage IAM            |
         |                v
         |          +-----------+
         +--------->|   Filer   |
                    +-----+-----+
                          |
                    +-----+-----+
                    |  Master   |
                    +--+--+--+--+
                       |  |  |
                 +-----+  |  +------+
                 v        v         v
            +--------+ +--------+ +--------+
            |Volume 1| |Volume 2| |Volume 3|
            +--------+ +--------+ +--------+

   Shared Cluster also includes:
     Route Registrar --> NATS (mTLS)
     OTel Collector  --> OTLP Endpoint
     Syslog Forwarder --> External Syslog
     Backup Agent    --> Local/NFS/S3
```

### On-Demand Dedicated Clusters

When a developer provisions a dedicated plan, the broker deploys an isolated SeaweedFS cluster via the BOSH Director. Each dedicated cluster gets its own master, volume, filer, S3 gateway, and admin console.

```
   Developer                     BOSH
   cf create-service             Director
        |                           ^
        v                           |
   Cloud Controller          2. Deploy Manifest
        |                           |
        v                           |
     GoRouter               +------+------+
        |                   |   Broker    |
        +------------------>| 1. Generate |
                            |   Manifest  |
                            +------+------+
                                   |
              +--------------------+--------------------+
              |                                         |
              v                                         v
   +---------------------+              +---------------------+
   | Dedicated Cluster A |              | Dedicated Cluster B |
   |   (Single Node)     |              |       (HA)          |
   |                     |              |                     |
   |  Master (1)         |              |  Master (3)         |
   |  Volume (1)         |              |  Volume (6)         |
   |  Filer  (1)         |              |  Filer  (3)         |
   |  S3 + IAM           |              |  S3 + IAM           |
   |  Admin              |              |  Admin              |
   |  BPM                |              |  BPM                |
   +---------------------+              +---------------------+
        |                                    |
        +--- Optional Route --> GoRouter <---+
```

### Service Binding & Credential Flow

Each `cf bind-service` creates an isolated IAM user with bucket-scoped access on either the shared or dedicated cluster.

```
   Developer            Cloud           Service          S3 Gateway
                      Controller        Broker           (IAM API)
      |                  |                |                  |
      | cf bind-service  |                |                  |
      |----------------->|                |                  |
      |                  | PUT /v2/...    |                  |
      |                  |--------------->|                  |
      |                  |                |                  |
      |                  |                | Create IAM User  |
      |                  |                |----------------->|
      |                  |                |       OK         |
      |                  |                |<-----------------|
      |                  |                |                  |
      |                  |                | Create Access Key|
      |                  |                |----------------->|
      |                  |                | Key + Secret     |
      |                  |                |<-----------------|
      |                  |                |                  |
      |                  |                | Attach Policy    |
      |                  |                |----------------->|
      |                  |                |       OK         |
      |                  |                |<-----------------|
      |                  |                |                  |
      |                  |   Credentials  |                  |
      |                  |<---------------|                  |
      |  Binding done    |                |                  |
      |<-----------------|                |                  |
      |                  |                |                  |
      |        Application uses S3 API with access_key      |
      |        (scoped to bound bucket only)                 |
      |----------------------------------------------------->|
```

## Components

| Component | Description |
|-----------|-------------|
| **seaweedfs-master** | Master server managing cluster topology and volume allocation |
| **seaweedfs-volume** | Volume server storing actual file data with rack-aware placement |
| **seaweedfs-filer** | Filer server providing POSIX-like file system interface (leveldb2 backend) |
| **seaweedfs-s3** | S3-compatible API gateway with embedded IAM for credential management |
| **seaweedfs-admin** | Password-protected admin console for cluster monitoring and management |
| **seaweedfs-broker** | Open Service Broker (v2.17) for Cloud Foundry with shared and dedicated plans |
| **route-registrar** | Registers routes with Cloud Foundry gorouter via NATS mTLS |
| **register-broker** | Post-deploy errand to register the service broker with CF |
| **deregister-broker** | Pre-delete errand to deregister the service broker |
| **smoke-tests** | Errand that validates S3 operations end-to-end for shared and on-demand plans |
| **otel-collector** | OpenTelemetry Collector that scrapes Prometheus metrics and exports via OTLP |
| **syslog-forwarder** | Forwards SeaweedFS logs to an external syslog endpoint |
| **backup-agent** | Scheduled backups of filer metadata and IAM config to local/NFS/S3 |
| **restore** | Errand to restore from a backup snapshot |
| **upgrade-all-service-instances** | Errand to roll BOSH updates across all on-demand dedicated clusters |

## Quick Start

### Building

```bash
# Build just the BOSH release
./scripts/build-release.sh 1.0.137

# Build the full Tanzu tile (includes BOSH release + dependencies)
./scripts/build-tile.sh

# Build tile with a specific version
./scripts/build-tile.sh 1.0.137

# Build tile using a pre-built release
./scripts/build-tile.sh --release releases/seaweedfs-1.0.137.tgz
```

The tile build script auto-increments the version if none is specified. Output goes to `product/seaweedfs-<version>.pivotal`.

### Deploying with BOSH (standalone)

```bash
# Upload the release
bosh upload-release releases/seaweedfs-<version>.tgz

# Single VM deployment (testing)
bosh -d seaweedfs deploy manifests/seaweedfs-single-vm.yml

# Multi-VM deployment (production)
bosh -d seaweedfs deploy manifests/seaweedfs.yml \
  -v seaweedfs_master_address=<master-ip> \
  -v seaweedfs_filer_address=<filer-ip>
```

### Deploying with Tanzu Operations Manager

1. Upload the `.pivotal` file via **Import a Product**
2. Click **+** to add SeaweedFS to the installation
3. Configure tile settings (see [Tile Configuration](#tile-configuration))
4. Click **Apply Changes**

## Service Broker

The SeaweedFS service broker implements the [Open Service Broker API](https://www.openservicebrokerapi.org/) v2.17 and provides two types of service plans:

### Shared Plan

- Creates a dedicated S3 bucket on the shared SeaweedFS cluster
- Generates unique IAM user and access keys per binding via SeaweedFS IAM API
- Bucket-scoped IAM policies restrict each binding to its own bucket
- Credentials are automatically cleaned up on unbind (user, access key, and policy deleted)
- Optional CredHub integration for secure credential storage
- Synchronous provisioning
- Ideal for development and small workloads

### Dedicated Plans (On-Demand)

- Provisions an isolated SeaweedFS cluster via BOSH
- Asynchronous provisioning (polls BOSH task until complete)
- Two deployment types:
  - **Single Node** (dev/test): 1 master, 1 volume, 1 filer
  - **High Availability** (production): 3 masters, 6 volumes, 3 filers
- Configurable VM types, persistent disk types, and storage quotas
- Optional route registration for S3, master, filer, volume, and admin console endpoints
- Per-binding IAM credentials on the dedicated cluster
- Automatic admin credential generation
- BPM process management on all instance groups

### Using the Service Broker

```bash
# Create a shared bucket
cf create-service seaweedfs shared my-bucket

# Create a dedicated cluster (on-demand plan names are operator-configured)
cf create-service seaweedfs "Dedicated S3 Cluster" my-cluster

# Bind to an application
cf bind-service my-app my-bucket

# View credentials
cf env my-app
```

### Binding Credentials

Each binding creates a dedicated IAM user with unique access keys:

```json
{
  "credentials": {
    "endpoint": "s3.sys.example.com",
    "endpoint_url": "https://s3.sys.example.com",
    "bucket": "cf-abc123-def456",
    "access_key": "AKIAEXAMPLE",
    "secret_key": "secretkey123",
    "region": "us-east-1",
    "use_ssl": true,
    "uri": "s3://AKIAEXAMPLE:secretkey123@s3.sys.example.com/cf-abc123-def456"
  }
}
```

For dedicated clusters with route registration enabled, bindings also include management URLs:

```json
{
  "credentials": {
    "console_url": "https://seaweedfs-console-abc12345.sys.example.com",
    "filer_url": "https://seaweedfs-filer-abc12345.sys.example.com",
    "admin_url": "https://seaweedfs-admin-abc12345.sys.example.com",
    "admin_username": "admin",
    "admin_password": "auto-generated-password"
  }
}
```

## Cloud Foundry Integration

### Route Registration

The tile registers routes with the Cloud Foundry gorouter. All routes are individually configurable:

| Route | Default Hostname | Description |
|-------|-----------------|-------------|
| S3 API | `s3` | S3-compatible endpoint for client access |
| Service Broker | `seaweedfs-broker` | OSB API endpoint |
| Master Console | `seaweedfs-master` | Master server management UI |
| Filer Console | `seaweedfs-filer` | Filer server management UI |
| Volume Console | `seaweedfs-volume` | Volume server management UI |
| Admin Console | `seaweedfs-admin` | Password-protected admin dashboard |

Routes are registered on the system domain (e.g., `s3.sys.example.com`). NATS mTLS is used for route communication.

For on-demand dedicated clusters, route registration uses property-based NATS configuration (machines, port, user/password, TLS certs) since cross-deployment BOSH links are not available.

### TLS/SSL

All endpoints are SSL-terminated at the gorouter. End-to-end TLS can be enabled for the S3 gateway:

```yaml
seaweedfs:
  s3:
    tls:
      enabled: true
      certificate: |
        -----BEGIN CERTIFICATE-----
        ...
      private_key: |
        -----BEGIN PRIVATE KEY-----
        ...
```

## Tile Configuration

The tile exposes the following configuration sections in Operations Manager:

### Shared Cluster Configuration

| Setting | Description | Default |
|---------|-------------|---------|
| Enable Shared Plan | Toggle the shared bucket plan on/off | Enabled |
| Plan Name / Description | Marketplace display for the shared plan | "shared" |
| Replication Strategy | No replication, same rack, different rack, or different DC | No replication |
| Max Volumes per Server | Controls total storage capacity (10-1000) | 100 |
| S3 Route Hostname | Hostname for the S3 endpoint route | `s3` |
| Broker Route Hostname | Hostname for the broker route | `seaweedfs-broker` |
| Console Routes | Enable/disable and configure hostnames for master, filer, volume, and admin consoles | Disabled |
| On-Demand AZs | Availability zones for dedicated cluster deployments | |

### On-Demand Service Plans

Operators can define multiple on-demand plans, each with:

| Setting | Description |
|---------|-------------|
| Plan Description | Shown to developers in the marketplace |
| Deployment Type | Single Node (dev/test) or High Availability (production) |
| VM Type | VM size for cluster nodes |
| Persistent Disk Type | Disk type for data storage |
| Storage Quota (GB) | Maximum storage per instance (10-10,000 GB) |
| Console Route Flags | Enable/disable route registration for master, filer, volume, and admin consoles |

### Observability

| Setting | Description | Default |
|---------|-------------|---------|
| OTLP Endpoint | OpenTelemetry collector export endpoint | (disabled) |
| OTLP Protocol | gRPC or HTTP | gRPC |
| Scrape Interval | Prometheus scrape interval | 30s |
| Host Metrics | Enable CPU/memory/disk/network metrics | Enabled |
| Syslog Address | External syslog drain host:port | (disabled) |
| Syslog TLS | Enable TLS for syslog transport | Disabled |

### Backup & Restore

| Setting | Description | Default |
|---------|-------------|---------|
| Enable Backups | Toggle scheduled backups | Disabled |
| Backup Schedule | Cron expression | `0 2 * * *` (2 AM daily) |
| Destination | local, nfs, or s3 | local |
| Retention Count | Number of backups to keep | 7 |

### Smoke Test Configuration

| Setting | Description | Default |
|---------|-------------|---------|
| Test On-Demand Plan | Also test an on-demand plan during smoke tests (adds 15-30+ min) | Disabled |
| On-Demand Plan Name | Which on-demand plan to test | |

## Properties Reference

### Master Server

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.master.port` | HTTP port | 9333 |
| `seaweedfs.master.port_grpc` | gRPC port | 19333 |
| `seaweedfs.master.volume_size_limit_mb` | Volume size limit | 30000 |
| `seaweedfs.master.default_replication` | Replication type | "000" |
| `seaweedfs.master.peers` | HA peer addresses | "" |
| `seaweedfs.master.metrics_port` | Prometheus metrics port | 9324 |

### Volume Server

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.volume.port` | HTTP port | 8080 |
| `seaweedfs.volume.port_grpc` | gRPC port | 18080 |
| `seaweedfs.volume.master` | Master server address | "localhost:9333" |
| `seaweedfs.volume.max_volumes` | Max volumes | 100 |
| `seaweedfs.volume.data_center` | Data center name | "" |
| `seaweedfs.volume.rack` | Rack name (auto-set from AZ) | "" |
| `seaweedfs.volume.metrics_port` | Prometheus metrics port | 9325 |

### Filer Server

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.filer.port` | HTTP port | 8888 |
| `seaweedfs.filer.port_grpc` | gRPC port | 18888 |
| `seaweedfs.filer.master` | Master server address | "localhost:9333" |
| `seaweedfs.filer.max_mb` | Max file size MB | 256 |
| `seaweedfs.filer.store` | Backend store type | "leveldb2" |
| `seaweedfs.filer.metrics_port` | Prometheus metrics port | 9326 |

### S3 Gateway

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.s3.port` | HTTP port | 8333 |
| `seaweedfs.s3.port_grpc` | gRPC port | 18333 |
| `seaweedfs.s3.filer` | Filer server address | "localhost:8888" |
| `seaweedfs.s3.iam.enabled` | Enable embedded IAM API | true |
| `seaweedfs.s3.tls.enabled` | Enable TLS | false |
| `seaweedfs.s3.config.enabled` | Enable S3 credentials config | false |
| `seaweedfs.s3.config.identities` | S3 access credentials | [] |
| `seaweedfs.s3.metrics_port` | Prometheus metrics port | 9327 |

### Admin Console

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.admin.port` | HTTP port | 23646 |
| `seaweedfs.admin.username` | Auth username | "admin" |
| `seaweedfs.admin.password` | Auth password | "" |

### Service Broker

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.broker.port` | Broker API port | 8080 |
| `seaweedfs.broker.auth.username` | Basic auth username | "broker" |
| `seaweedfs.broker.auth.password` | Basic auth password | (required) |
| `seaweedfs.broker.tls.enabled` | Enable TLS | false |
| `seaweedfs.broker.catalog.plans` | Service plans configuration | (see spec) |
| `seaweedfs.broker.shared_cluster.*` | Shared cluster connection | (see spec) |
| `seaweedfs.broker.bosh.*` | BOSH director connection | (see spec) |

## Replication Types

SeaweedFS uses a 3-digit replication type:

| Type | Description |
|------|-------------|
| `000` | No replication |
| `001` | Replicate once on the same rack |
| `010` | Replicate once on different rack, same data center |
| `100` | Replicate once on different data center |
| `110` | Replicate twice (different rack + different DC) |
| `200` | Replicate twice on different data centers |

## Monitoring

All components expose Prometheus metrics, scraped by the co-located OTel Collector:

| Component | Metrics Port |
|-----------|-------------|
| Master | 9324 |
| Volume | 9325 |
| Filer | 9326 |
| S3 | 9327 |

When an OTLP endpoint is configured, the OTel Collector exports metrics via gRPC or HTTP. Host-level metrics (CPU, memory, disk, network) are also available.

## CI/CD

The project includes a GitHub Actions pipeline for automated builds:

| Workflow | Trigger | Description |
|----------|---------|-------------|
| **upstream-watch** | Scheduled | Monitors SeaweedFS releases and dispatches build events |
| **build-and-release** | Dispatch / Manual | Builds BOSH release, packages tile, creates GitHub Release |
| **dependency-tracker** | Scheduled | Tracks dependency versions and creates update issues |

## Project Structure

```
bosh-seaweedfs/
+-- jobs/
|   +-- seaweedfs-master/               Master server (BPM-managed)
|   +-- seaweedfs-volume/               Volume server (BPM-managed)
|   +-- seaweedfs-filer/                Filer server (BPM-managed, wrapper script)
|   +-- seaweedfs-s3/                   S3 gateway (BPM-managed)
|   +-- seaweedfs-admin/                Admin console (ctl-managed)
|   +-- seaweedfs-broker/               Service broker (Go)
|   +-- route-registrar/                CF route registration
|   +-- register-broker/                Broker registration errand
|   +-- deregister-broker/              Broker deregistration errand
|   +-- smoke-tests/                    S3 validation errand
|   +-- otel-collector/                 OpenTelemetry metrics collector
|   +-- syslog-forwarder/              Log forwarding to external syslog
|   +-- backup-agent/                   Scheduled backup agent
|   +-- restore/                        Backup restore errand
|   +-- upgrade-all-service-instances/  On-demand cluster upgrade errand
+-- packages/
|   +-- seaweedfs/                      SeaweedFS binary
|   +-- seaweedfs-broker/               Go broker binary
|   +-- golang-1.21/                    Go toolchain
|   +-- cf-cli/                         CF CLI for errands
|   +-- smoke-tests-vendor/             Vendored Python deps
|   +-- otel-collector/                 OpenTelemetry Collector binary
+-- src/
|   +-- seaweedfs-broker/               Go service broker source
+-- scripts/
|   +-- build-tile.sh                   Full tile build (release + deps + tile-generator)
|   +-- build-release.sh                BOSH release build
|   +-- build-tile-dev.sh               Dev tile build (no network deps)
|   +-- get-next-version.sh             Auto-increment version from tile.yml
|   +-- add-blob.sh                     Download and add SeaweedFS blob
|   +-- fetch-upstream.sh               Fetch latest upstream SeaweedFS release
|   +-- vendor-smoke-test-deps.sh       Vendor Python deps for smoke tests
|   +-- vendor-dependencies.sh          Vendor Go + other dependencies
+-- .github/workflows/
|   +-- build-and-release.yml           Unified build pipeline
|   +-- 01-upstream-watch.yml           Upstream version monitor
|   +-- dependency-tracker.yml          Dependency update tracker
+-- tile.yml                            Tanzu tile configuration (Kiln format)
+-- resources/
|   +-- icon.png                        Marketplace icon
+-- config/
    +-- blobs.yml                       BOSH blob tracking
    +-- final.yml                       BOSH release config
```

## Development

### Building the Broker

```bash
cd src/seaweedfs-broker
go build ./...
go vet ./...
```

### Adding a New SeaweedFS Version

```bash
./scripts/add-blob.sh <version>   # e.g., 4.07
```

### Vendoring Smoke Test Dependencies

```bash
./scripts/vendor-smoke-test-deps.sh
```

## License

Apache 2.0
