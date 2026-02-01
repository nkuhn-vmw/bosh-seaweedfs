# BOSH Release for SeaweedFS

This BOSH release deploys [SeaweedFS](https://github.com/seaweedfs/seaweedfs), a fast distributed storage system for blobs, objects, files, and data lake.

## Components

- **seaweedfs-master**: Master server that manages cluster topology and volume allocation
- **seaweedfs-volume**: Volume server that stores actual file data
- **seaweedfs-filer**: Filer server providing POSIX-like file system interface
- **seaweedfs-s3**: S3-compatible API gateway

## Usage

### 1. Add the SeaweedFS blob

```bash
./scripts/add-blob.sh 4.07
```

### 2. Create the release

```bash
bosh create-release --force
```

### 3. Upload the release

```bash
bosh upload-release
```

### 4. Deploy

Single VM deployment (for testing):
```bash
bosh -d seaweedfs deploy manifests/seaweedfs-single-vm.yml
```

Multi-VM deployment (production):
```bash
bosh -d seaweedfs deploy manifests/seaweedfs.yml \
  -v seaweedfs_master_address=<master-ip> \
  -v seaweedfs_filer_address=<filer-ip>
```

## Properties

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
| `seaweedfs.s3.config.enabled` | Enable S3 credentials config | false |
| `seaweedfs.s3.config.identities` | S3 access credentials | [] |
| `seaweedfs.s3.metrics_port` | Prometheus metrics port | 9327 |

## Replication Types

SeaweedFS uses a 3-digit replication type:

- `000`: No replication
- `001`: Replicate once on the same rack
- `010`: Replicate once on different rack, same data center
- `100`: Replicate once on different data center
- `110`: Replicate twice, one on different rack and one on different data center

## High Availability

For production deployments:

1. Deploy 3+ master servers with `seaweedfs.master.peers` configured
2. Deploy multiple volume servers across different racks/data centers
3. Use replication type `100` or higher
4. Deploy multiple filer servers for read scaling

## Accessing Services

After deployment:

- Master UI: `http://<master-ip>:9333`
- Filer UI: `http://<filer-ip>:8888`
- S3 API: `http://<s3-ip>:8333`

## License

Apache 2.0
