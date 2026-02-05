# BOSH Release for SeaweedFS

This BOSH release deploys [SeaweedFS](https://github.com/seaweedfs/seaweedfs), a fast distributed storage system for blobs, objects, files, and data lake.

## Features

- **Distributed Object Storage**: SeaweedFS master, volume, and filer servers
- **S3-Compatible API**: Full S3 gateway with IAM-based credential management
- **Per-Binding IAM Credentials**: Each service binding gets its own isolated IAM user and access keys via SeaweedFS's embedded IAM API
- **Open Service Broker**: Provision shared buckets or dedicated clusters on-demand
- **Cloud Foundry Integration**: Route registration via Go Router with TLS termination
- **Smoke Tests**: Automated errand validates end-to-end S3 connectivity (put, get, list, delete)
- **Tanzu Operations Manager Tile**: One-click deployment for Tanzu platform

## Components

| Component | Description |
|-----------|-------------|
| **seaweedfs-master** | Master server managing cluster topology and volume allocation |
| **seaweedfs-volume** | Volume server storing actual file data |
| **seaweedfs-filer** | Filer server providing POSIX-like file system interface |
| **seaweedfs-s3** | S3-compatible API gateway |
| **seaweedfs-broker** | Open Service Broker for CF/Kubernetes integration |
| **route-registrar** | Cloud Foundry route registration for SSL/TLS |
| **register-broker** | Errand to register the service broker with CF |
| **deregister-broker** | Errand to deregister the service broker |
| **smoke-tests** | Errand that validates S3 operations end-to-end |

## Quick Start

### 1. Add the SeaweedFS blob

```bash
./scripts/add-blob.sh 4.07
```

### 2. Create and upload the release

```bash
bosh create-release --force
bosh upload-release
```

### 3. Deploy

**Single VM deployment (testing):**
```bash
bosh -d seaweedfs deploy manifests/seaweedfs-single-vm.yml
```

**Multi-VM deployment (production):**
```bash
bosh -d seaweedfs deploy manifests/seaweedfs.yml \
  -v seaweedfs_master_address=<master-ip> \
  -v seaweedfs_filer_address=<filer-ip>
```

## Service Broker

The SeaweedFS service broker implements the [Open Service Broker API](https://www.openservicebrokerapi.org/) and provides two service plans:

### Shared Plan
- Creates a dedicated S3 bucket on the shared SeaweedFS cluster
- Generates unique IAM user and access keys per binding via SeaweedFS IAM API
- Bucket-scoped IAM policies restrict each binding to its own bucket
- Credentials are automatically cleaned up on unbind (user, access key, and policy deleted)
- Ideal for development and small workloads

### Dedicated Plans
- Provisions an isolated SeaweedFS cluster via BOSH
- Full cluster isolation with configurable sizing
- Supports small (1 master, 3 volumes) and large (3 masters, 6 volumes) configurations

### Using the Service Broker with Cloud Foundry

```bash
# Register the broker
cf create-service-broker seaweedfs broker-user broker-password https://seaweedfs-broker.apps.example.com

# Enable service access
cf enable-service-access seaweedfs

# Create a service instance (shared bucket)
cf create-service seaweedfs shared my-bucket

# Create a dedicated cluster
cf create-service seaweedfs dedicated-small my-cluster

# Bind to an application
cf bind-service my-app my-bucket

# View credentials
cf env my-app
```

### Binding Credentials

When you bind a service instance, the broker creates a dedicated IAM user with its own access keys. Each binding receives unique, isolated credentials:

```json
{
  "credentials": {
    "endpoint": "s3.seaweedfs.example.com",
    "endpoint_url": "https://s3.seaweedfs.example.com",
    "bucket": "cf-abc123-def456",
    "access_key": "AKIAEXAMPLE",
    "secret_key": "secretkey123",
    "region": "us-east-1",
    "use_ssl": true,
    "uri": "s3://AKIAEXAMPLE:secretkey123@s3.seaweedfs.example.com/cf-abc123-def456"
  }
}
```

## Cloud Foundry Integration

### Route Registration

The tile automatically registers routes with the Cloud Foundry Go Router:

- **S3 Endpoint**: `s3.apps.example.com` (configurable)
- **Service Broker**: `seaweedfs-broker.apps.example.com` (configurable)

### TLS/SSL

All endpoints are SSL-terminated at the Go Router level. You can also enable end-to-end TLS by setting:

```yaml
seaweedfs:
  s3:
    tls:
      enabled: true
      certificate: |
        -----BEGIN CERTIFICATE-----
        ...
        -----END CERTIFICATE-----
      private_key: |
        -----BEGIN PRIVATE KEY-----
        ...
        -----END PRIVATE KEY-----
```

## Tanzu Operations Manager Tile

### Building the Tile

```bash
# Production build (downloads dependencies)
./scripts/build-tile.sh

# Development build (no network required)
./scripts/build-tile-dev.sh
```

The tile is output to `product/seaweedfs-<version>.pivotal`.

### Installing the Tile

1. Log into Tanzu Operations Manager
2. Click "Import a Product" and upload the `.pivotal` file
3. Click the "+" button next to SeaweedFS to add it to the installation
4. Configure the tile settings:
   - Cluster configuration (node counts, replication)
   - Service broker credentials
   - Network routes
   - TLS certificates
5. Click "Apply Changes"

### Tile Configuration

| Section | Settings |
|---------|----------|
| Cluster Configuration | Master count, volume count, replication type |
| Service Broker | Username, password, catalog plans |
| Networking | S3 route hostname, broker route hostname, TLS |

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
| `seaweedfs.volume.rack` | Rack name | "" |
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
| `seaweedfs.s3.iam.enabled` | Enable embedded IAM API for dynamic credentials | true |
| `seaweedfs.s3.tls.enabled` | Enable TLS | false |
| `seaweedfs.s3.config.enabled` | Enable S3 credentials config | false |
| `seaweedfs.s3.config.identities` | S3 access credentials | [] |
| `seaweedfs.s3.metrics_port` | Prometheus metrics port | 9327 |

### Service Broker

| Property | Description | Default |
|----------|-------------|---------|
| `seaweedfs.broker.port` | Broker API port | 8080 |
| `seaweedfs.broker.auth.username` | Basic auth username | "broker" |
| `seaweedfs.broker.auth.password` | Basic auth password | (required) |
| `seaweedfs.broker.tls.enabled` | Enable TLS | false |
| `seaweedfs.broker.catalog.plans` | Service plans configuration | (see defaults) |
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

## High Availability

For production deployments:

1. Deploy 3+ master servers with `seaweedfs.master.peers` configured
2. Deploy multiple volume servers across different racks/data centers
3. Use replication type `100` or higher
4. Deploy multiple filer servers for read scaling
5. Use the service broker for on-demand cluster provisioning

## Monitoring

All components expose Prometheus metrics:

| Component | Metrics Port |
|-----------|-------------|
| Master | 9324 |
| Volume | 9325 |
| Filer | 9326 |
| S3 | 9327 |

## Accessing Services

After deployment:

- **Master UI**: `http://<master-ip>:9333`
- **Filer UI**: `http://<filer-ip>:8888`
- **S3 API**: `http://<s3-ip>:8333` or via CF route

## Development

### Project Structure

```
bosh-seaweedfs/
├── jobs/
│   ├── seaweedfs-master/
│   ├── seaweedfs-volume/
│   ├── seaweedfs-filer/
│   ├── seaweedfs-s3/
│   ├── seaweedfs-broker/
│   ├── smoke-tests/
│   ├── route-registrar/
│   ├── register-broker/
│   └── deregister-broker/
├── packages/
│   ├── seaweedfs/
│   ├── seaweedfs-broker/
│   └── golang-1.21/
├── src/
│   └── seaweedfs-broker/     # Go service broker source
├── tile/
│   └── metadata/             # Tanzu tile configuration
├── manifests/
│   ├── seaweedfs.yml         # Multi-VM manifest
│   └── seaweedfs-single-vm.yml
├── scripts/
│   ├── add-blob.sh
│   ├── build-tile.sh
│   └── build-tile-dev.sh
└── config/
    ├── blobs.yml
    └── final.yml
```

### Building the Broker

```bash
cd src/seaweedfs-broker
go mod download
go build -o seaweedfs-broker .
```

## License

Apache 2.0
