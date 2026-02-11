# SeaweedFS BOSH Tile — Technical Build Spec

**New Feature Implementation Guide**

Version 1.0 — February 2026 | Repository: `github.com/nkuhn-vmw/bosh-seaweedfs`

---

## Table of Contents

- [1. Scope](#1-scope)
- [2. BOSH Release Hardening](#2-bosh-release-hardening)
- [3. Observability: Syslog Drain](#3-observability-syslog-drain)
- [4. Observability: OpenTelemetry Metrics](#4-observability-opentelemetry-metrics)
- [5. Security: CredHub Integration](#5-security-credhub-integration)
- [6. Security: mTLS Between Components](#6-security-mtls-between-components)
- [7. Security: Certificate Rotation](#7-security-certificate-rotation)
- [8. Backup and Restore](#8-backup-and-restore)
- [9. Smoke Test Expansion](#9-smoke-test-expansion)
- [10. Upgrade All Service Instances Errand](#10-upgrade-all-service-instances-errand)
- [11. Tile Configuration Forms](#11-tile-configuration-forms)
- [12. Air-Gap Vendoring Completion](#12-air-gap-vendoring-completion)
- [13. CI/CD Pipeline](#13-cicd-pipeline)
- [14. Supply Chain Security](#14-supply-chain-security)
- [15. Implementation Phases](#15-implementation-phases)

---

## 1. Scope

This spec covers **only the features to be added** to the existing SeaweedFS BOSH tile. The following are already implemented and out of scope:

- SeaweedFS core jobs (master, volume, filer, S3 gateway)
- Service broker with shared and dedicated plans
- Per-binding IAM credential management
- GoRouter route registration with TLS termination
- Basic smoke tests (S3 CRUD)
- Management dashboard
- Per-instance service dashboards
- Tile packaging scripts (`build-tile.sh`, `build-tile-dev.sh`)
- Blob add script (`add-blob.sh`)
- Register/deregister broker errands

### 1.1 Feature Summary

| # | Feature | Priority | Effort |
|---|---------|----------|--------|
| 2 | BOSH Release Hardening (BPM, Links, Rack-to-AZ) | P0 | 2 weeks |
| 3 | Syslog Drain Support | P0 | < 1 week |
| 4 | OpenTelemetry Metrics | P1 | 1–2 weeks |
| 5 | CredHub Integration | P0 | 1–2 weeks |
| 6 | mTLS Between Components | P1 | 1–2 weeks |
| 7 | Certificate Rotation | P1 | 1 week |
| 8 | Backup and Restore | P1 | 2–3 weeks |
| 9 | Smoke Test Expansion | P2 | 2 weeks |
| 10 | Upgrade All Instances Errand | P1 | 1–2 weeks |
| 11 | Tile Configuration Forms | P1 | 1 week |
| 12 | Air-Gap Vendoring Completion | P0 | < 1 week |
| 13 | CI/CD Pipeline (4-stage) | P2 | 3–4 weeks |
| 14 | Supply Chain Security | P2 | < 1 week |

---

## 2. BOSH Release Hardening

### 2.1 BPM Integration

All SeaweedFS jobs should run under BOSH Process Manager for consistent process isolation, resource limits, and logging.

**Implementation:**

For each existing job (`seaweedfs-master`, `seaweedfs-volume`, `seaweedfs-filer`, `seaweedfs-s3`, `seaweedfs-broker`), add a `bpm.yml.erb` template.

Example for `seaweedfs-master`:

```yaml
# jobs/seaweedfs-master/templates/bpm.yml.erb
processes:
  - name: seaweedfs-master
    executable: /var/vcap/packages/seaweedfs/weed
    args:
      - master
      - -ip=<%= spec.address %>
      - -port=<%= p('port') %>
      - -grpcPort=<%= p('grpc_port') %>
      - -defaultReplication=<%= p('default_replication') %>
      - -volumeSizeLimitMB=<%= p('volume_size_limit_mb') %>
      - -peers=<%= link('master').instances.map { |i| "#{i.address}:#{p('port')}" }.join(',') %>
      - -mdir=/var/vcap/store/seaweedfs-master
    env:
      GOGC: "80"
    limits:
      open_files: 65536
    persistent_disk: true
    additional_volumes:
      - path: /var/vcap/store/seaweedfs-master
        writable: true
```

Update each job's `monit` file to use BPM:

```
check process seaweedfs-master
  with pidfile /var/vcap/sys/run/bpm/seaweedfs-master/seaweedfs-master.pid
  start program "/var/vcap/jobs/bpm/bin/bpm start seaweedfs-master"
  stop program "/var/vcap/jobs/bpm/bin/bpm stop seaweedfs-master"
  group vcap
```

Add BPM as a dependency in each job spec:

```yaml
# jobs/seaweedfs-master/spec
templates:
  bpm.yml.erb: config/bpm.yml
packages: []
consumes:
  - name: master
    type: master
provides:
  - name: master
    type: master
    properties:
      - port
      - grpc_port
```

**Package dependency:** The release must depend on `bpm-release`. Add to deployment manifest:

```yaml
releases:
  - name: bpm
    version: latest
```

### 2.2 BOSH Links

Implement BOSH links to auto-wire component addresses and ports, eliminating manual IP configuration.

**Link topology:**

| Job | Provides | Consumes |
|-----|----------|----------|
| `seaweedfs-master` | `master` (addresses, port, grpc_port) | — |
| `seaweedfs-volume` | — | `master` |
| `seaweedfs-filer` | `filer` (addresses, port, s3_port) | `master` |
| `seaweedfs-s3` | — | `master`, `filer` |
| `seaweedfs-broker` | — | `master`, `filer` |
| `otel-collector` | — | `master`, `filer` (for metrics endpoint discovery) |
| `route-registrar` | — | `nats` (cross-deployment link from CF) |

**Example: seaweedfs-volume consuming the master link:**

```yaml
# jobs/seaweedfs-volume/spec
consumes:
  - name: master
    type: master

properties:
  port:
    description: "Volume server HTTP port"
    default: 8080
  grpc_port:
    description: "Volume server gRPC port"
    default: 18080
  max_volumes:
    description: "Maximum number of volumes per server"
    default: 100
  compaction_threshold:
    description: "Vacuum compaction threshold (0.0-1.0)"
    default: 0.3
```

```erb
# jobs/seaweedfs-volume/templates/bpm.yml.erb
<% master = link('master') %>
processes:
  - name: seaweedfs-volume
    executable: /var/vcap/packages/seaweedfs/weed
    args:
      - volume
      - -ip=<%= spec.address %>
      - -port=<%= p('port') %>
      - -grpcPort=<%= p('grpc_port') %>
      - -mserver=<%= master.instances.map { |i| "#{i.address}:#{master.p('port')}" }.join(',') %>
      - -dir=/var/vcap/store/seaweedfs-volume
      - -max=<%= p('max_volumes') %>
      - -rack=<%= spec.az %>
      - -compactionMBps=50
    persistent_disk: true
    additional_volumes:
      - path: /var/vcap/store/seaweedfs-volume
        writable: true
```

### 2.3 Rack-to-AZ Mapping

Map BOSH availability zones to SeaweedFS rack topology so replication spans failure domains.

**Implementation:** In every volume server and master server BPM template, pass `spec.az` as the `-rack` flag:

```erb
- -rack=<%= spec.az %>
```

This is a one-line change per template but has significant data durability impact. When the replication policy is `001` (replicate to one other rack), data is automatically replicated across BOSH AZs.

**Validation:** After deployment, verify topology via the master API:

```bash
curl http://<master>:9333/cluster/status
```

The response should show volume servers grouped by rack, with rack names matching BOSH AZ names.

### 2.4 Consolidate S3 into Filer (Optional)

The current repo has a separate `seaweedfs-s3` job. SeaweedFS natively supports running the S3 gateway as part of the filer process. Consolidating reduces operational complexity (fewer VMs, simpler topology).

**If consolidating:** Add S3 flags to the filer BPM template:

```erb
- -s3
- -s3.port=<%= p('s3_port') %>
- -s3.domainName=<%= p('s3_domain_name') %>
```

Remove the standalone `seaweedfs-s3` job and update the route-registrar to point to the filer's S3 port.

**If keeping separate:** No action required. Both approaches are valid; the tradeoff is simplicity vs. independent scaling of the S3 gateway.

---

## 3. Observability: Syslog Drain

### 3.1 Job: `syslog-forwarder`

Create a colocated BOSH job that configures rsyslog to forward all SeaweedFS logs to an operator-specified syslog drain.

**Colocation:** This job must be colocated on every SeaweedFS VM — master, volume, filer, S3, broker. For on-demand clusters, the broker injects this job into the on-demand deployment manifest.

**Job spec:**

```yaml
# jobs/syslog-forwarder/spec
name: syslog-forwarder

templates:
  rsyslog.conf.erb: config/rsyslog.conf
  pre-start.erb: bin/pre-start

properties:
  syslog.address:
    description: "Syslog drain host:port"
  syslog.transport:
    description: "Transport protocol"
    default: tcp
    enum: [tcp, udp, relp]
  syslog.tls_enabled:
    description: "Enable TLS for syslog transport"
    default: false
  syslog.ca_cert:
    description: "CA certificate for TLS syslog"
    default: ""
  syslog.permitted_peer:
    description: "Permitted peer for TLS verification"
    default: ""
```

**rsyslog configuration template:**

```erb
# jobs/syslog-forwarder/templates/rsyslog.conf.erb
# Forward SeaweedFS logs and system logs to remote syslog drain

module(load="imfile")

<% if p('syslog.tls_enabled') %>
global(
  defaultNetstreamDriverCAFile="/var/vcap/jobs/syslog-forwarder/config/ca.pem"
)
$DefaultNetstreamDriver gtls
$ActionSendStreamDriverMode 1
$ActionSendStreamDriverAuthMode x509/name
$ActionSendStreamDriverPermittedPeer <%= p('syslog.permitted_peer') %>
<% end %>

# SeaweedFS process logs
input(type="imfile"
  File="/var/vcap/sys/log/seaweedfs-*/*.log"
  Tag="seaweedfs"
  Facility="local0"
  Severity="info")

# Structured syslog format with BOSH metadata
template(name="CfFormat" type="string"
  string="<%=134%>1 %timegenerated:::date-rfc3339% %HOSTNAME% %syslogtag% - - [instance@47450 deployment=\"<%= spec.deployment %>\" job=\"<%= spec.name %>\" index=\"<%= spec.index %>\" az=\"<%= spec.az %>\" id=\"<%= spec.id %>\"] %msg%\n")

# Forward to drain
<% if p('syslog.transport') == 'tcp' %>
action(type="omfwd" target="<%= p('syslog.address').split(':')[0] %>"
  port="<%= p('syslog.address').split(':')[1] %>"
  protocol="tcp" template="CfFormat"
  queue.type="LinkedList" queue.size="50000"
  action.resumeRetryCount="-1")
<% elsif p('syslog.transport') == 'udp' %>
action(type="omfwd" target="<%= p('syslog.address').split(':')[0] %>"
  port="<%= p('syslog.address').split(':')[1] %>"
  protocol="udp" template="CfFormat")
<% end %>
```

**Alternatively**, if the existing CF deployment uses `syslog-release` (from `cloudfoundry/syslog-release`), the tile can add it as a release dependency and colocate its `syslog_forwarder` job instead of building a custom one. This is the recommended approach for consistency with the platform.

---

## 4. Observability: OpenTelemetry Metrics

### 4.1 Job: `otel-collector`

Create a colocated BOSH job that runs the OpenTelemetry Collector, scraping Prometheus metrics from SeaweedFS components and forwarding them to an OTLP endpoint.

**Colocation:** Colocated on every SeaweedFS VM (master, volume, filer).

**Package: `otel-collector`**

Vendor the pre-compiled OpenTelemetry Collector binary (`otelcol-contrib`) for `linux/amd64` in the BOSH release blobs:

```bash
# Download and add as blob
wget https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.96.0/otelcol-contrib_0.96.0_linux_amd64.tar.gz
bosh add-blob otelcol-contrib_0.96.0_linux_amd64.tar.gz otel-collector/otelcol-contrib.tar.gz
```

Packaging script:

```bash
# packages/otel-collector/packaging
set -e
tar xzf otel-collector/otelcol-contrib.tar.gz
cp otelcol-contrib ${BOSH_INSTALL_TARGET}/
chmod +x ${BOSH_INSTALL_TARGET}/otelcol-contrib
```

**Job spec:**

```yaml
# jobs/otel-collector/spec
name: otel-collector

templates:
  bpm.yml.erb: config/bpm.yml
  otel-config.yml.erb: config/otel-config.yml

packages:
  - otel-collector

consumes:
  - name: master
    type: master
    optional: true
  - name: filer
    type: filer
    optional: true

properties:
  otel.otlp_endpoint:
    description: "OTLP exporter endpoint URL"
  otel.otlp_protocol:
    description: "OTLP protocol (grpc or http)"
    default: grpc
  otel.otlp_ca_cert:
    description: "TLS CA cert for OTLP endpoint"
    default: ""
  otel.otlp_headers:
    description: "Authentication headers for OTLP endpoint (key=value pairs)"
    default: {}
  otel.scrape_interval:
    description: "Prometheus scrape interval"
    default: "30s"
  otel.enable_host_metrics:
    description: "Enable host-level CPU/memory/disk/network metrics"
    default: true
```

**OTel Collector configuration template:**

```erb
# jobs/otel-collector/templates/otel-config.yml.erb
receivers:
  prometheus:
    config:
      scrape_configs:
        <% if_link('master') do |master| %>
        - job_name: seaweedfs-master
          scrape_interval: <%= p('otel.scrape_interval') %>
          static_configs:
            - targets: ['<%= spec.address %>:9333']
              labels:
                deployment: '<%= spec.deployment %>'
                job: '<%= spec.name %>'
                index: '<%= spec.index %>'
                az: '<%= spec.az %>'
                plan_type: 'shared'
        <% end %>
        <% if_link('filer') do |filer| %>
        - job_name: seaweedfs-filer
          scrape_interval: <%= p('otel.scrape_interval') %>
          static_configs:
            - targets: ['<%= spec.address %>:8888']
              labels:
                deployment: '<%= spec.deployment %>'
                job: '<%= spec.name %>'
                index: '<%= spec.index %>'
                az: '<%= spec.az %>'
        <% end %>
        - job_name: seaweedfs-volume
          scrape_interval: <%= p('otel.scrape_interval') %>
          static_configs:
            - targets: ['<%= spec.address %>:8080']
              labels:
                deployment: '<%= spec.deployment %>'
                job: '<%= spec.name %>'
                index: '<%= spec.index %>'
                az: '<%= spec.az %>'

  <% if p('otel.enable_host_metrics') %>
  hostmetrics:
    collection_interval: <%= p('otel.scrape_interval') %>
    scrapers:
      cpu: {}
      memory: {}
      disk: {}
      network: {}
      filesystem: {}
  <% end %>

processors:
  batch:
    timeout: 10s
    send_batch_size: 1024
  resourcedetection:
    detectors: [env]
  attributes:
    actions:
      - key: deployment
        value: '<%= spec.deployment %>'
        action: upsert
      - key: instance_id
        value: '<%= spec.id %>'
        action: upsert

exporters:
  otlp:
    endpoint: '<%= p('otel.otlp_endpoint') %>'
    <% if p('otel.otlp_protocol') == 'http' %>
    protocol: http/protobuf
    <% end %>
    <% if p('otel.otlp_ca_cert') != '' %>
    tls:
      ca_file: /var/vcap/jobs/otel-collector/config/ca.pem
    <% end %>
    <% if p('otel.otlp_headers') != {} %>
    headers:
      <% p('otel.otlp_headers').each do |k, v| %>
      <%= k %>: '<%= v %>'
      <% end %>
    <% end %>

service:
  pipelines:
    metrics:
      receivers: [prometheus<%= p('otel.enable_host_metrics') ? ', hostmetrics' : '' %>]
      processors: [resourcedetection, attributes, batch]
      exporters: [otlp]
```

### 4.2 Key SeaweedFS Metrics

SeaweedFS exposes Prometheus metrics on `/metrics` for each component. Key metrics to ensure are being scraped:

| Component | Metric | Description |
|-----------|--------|-------------|
| Master | `seaweedfs_master_volumes_total` | Total volume count |
| Master | `seaweedfs_master_free_volumes` | Free volume count |
| Master | `seaweedfs_master_is_leader` | Leader status (0/1) |
| Volume | `seaweedfs_volume_disk_used_bytes` | Disk usage per volume server |
| Volume | `seaweedfs_volume_read_ops_total` | Read operations counter |
| Volume | `seaweedfs_volume_write_ops_total` | Write operations counter |
| Filer/S3 | `seaweedfs_s3_request_total` | S3 requests by operation |
| Filer/S3 | `seaweedfs_s3_request_duration_seconds` | S3 latency histogram |
| Filer/S3 | `seaweedfs_s3_errors_total` | S3 error counter |

---

## 5. Security: CredHub Integration

### 5.1 Scope

All sensitive credentials must be stored in CredHub, never in plaintext on disk or in manifests. The following credentials require migration:

| Credential | Current State | Target |
|------------|--------------|--------|
| Broker auth (username/password) | Likely in tile properties | CredHub type `user` |
| Shared cluster S3 admin access key | Likely in tile properties | CredHub type `password` |
| Shared cluster S3 admin secret key | Likely in tile properties | CredHub type `password` |
| Per-instance S3 credentials | Created by broker | Broker stores in CredHub via API |
| BOSH Director UAA client (on-demand) | Likely in tile properties | CredHub type `user` |
| CF UAA client (route registration) | Likely in tile properties | CredHub type `user` |

### 5.2 BOSH Manifest Variables

Declare credential variables in the deployment manifest so BOSH auto-generates them in CredHub at first deploy:

```yaml
variables:
  - name: seaweedfs-broker-password
    type: password
  - name: seaweedfs-s3-admin-access-key
    type: password
    options:
      length: 20
  - name: seaweedfs-s3-admin-secret-key
    type: password
    options:
      length: 40
```

Reference in job properties:

```yaml
instance_groups:
  - name: seaweedfs-broker
    jobs:
      - name: seaweedfs-broker
        properties:
          broker:
            username: broker-admin
            password: ((seaweedfs-broker-password))
          shared_cluster:
            admin_access_key: ((seaweedfs-s3-admin-access-key))
            admin_secret_key: ((seaweedfs-s3-admin-secret-key))
```

### 5.3 Broker CredHub API Integration

The broker should use the CredHub API to store per-service-instance credentials created during bind operations. The broker needs a CredHub client (Go library: `code.cloudfoundry.org/credhub-cli/credhub`).

**On bind:**

```go
// Store generated credentials in CredHub
credPath := fmt.Sprintf("/seaweedfs-broker/instances/%s/bindings/%s", instanceID, bindingID)
_, err := credhubClient.SetJSON(credPath, values.JSON{
    "access_key": accessKey,
    "secret_key": secretKey,
    "bucket":     bucketName,
})
```

**On unbind:**

```go
err := credhubClient.Delete(credPath)
```

**Broker CredHub connection** should use mutual TLS with the BOSH-generated broker certificate, authenticated via UAA client credentials configured in the tile.

---

## 6. Security: mTLS Between Components

### 6.1 Certificate Variables

Declare a CA and leaf certificates for inter-component communication:

```yaml
variables:
  - name: seaweedfs-ca
    type: certificate
    options:
      is_ca: true
      common_name: SeaweedFS Internal CA

  - name: seaweedfs-master-tls
    type: certificate
    options:
      ca: seaweedfs-ca
      common_name: seaweedfs-master
      alternative_names:
        - "*.seaweedfs-master.default.seaweedfs.bosh"
        - "seaweedfs-master"

  - name: seaweedfs-volume-tls
    type: certificate
    options:
      ca: seaweedfs-ca
      common_name: seaweedfs-volume

  - name: seaweedfs-filer-tls
    type: certificate
    options:
      ca: seaweedfs-ca
      common_name: seaweedfs-filer
```

### 6.2 Job Template Changes

Add TLS properties to each job spec and pass them to SeaweedFS processes:

```yaml
# jobs/seaweedfs-master/spec (add to properties)
properties:
  tls.ca:
    description: "CA certificate for mTLS"
  tls.certificate:
    description: "Server certificate"
  tls.private_key:
    description: "Server private key"
```

In the BPM template, write certs to disk and reference them:

```erb
<%
  ca_cert_path = "/var/vcap/jobs/seaweedfs-master/config/certs/ca.pem"
  cert_path = "/var/vcap/jobs/seaweedfs-master/config/certs/cert.pem"
  key_path = "/var/vcap/jobs/seaweedfs-master/config/certs/key.pem"
%>
processes:
  - name: seaweedfs-master
    executable: /var/vcap/packages/seaweedfs/weed
    args:
      # ... existing args ...
      <% if_p('tls.ca') do %>
      - -security.tls.ca=<%= ca_cert_path %>
      - -security.tls.cert=<%= cert_path %>
      - -security.tls.key=<%= key_path %>
      <% end %>
```

Wire the cert variables in the deployment manifest:

```yaml
- name: seaweedfs-master
  jobs:
    - name: seaweedfs-master
      properties:
        tls:
          ca: ((seaweedfs-ca.ca))
          certificate: ((seaweedfs-master-tls.certificate))
          private_key: ((seaweedfs-master-tls.private_key))
```

---

## 7. Security: Certificate Rotation

### 7.1 Approach

Use the standard BOSH/CredHub transitional CA pattern for zero-downtime certificate rotation:

1. **Add transitional CA:** Mark the new CA as `transitional: true` in CredHub. BOSH concatenates both CAs into trust bundles.
2. **Regenerate leaf certs:** Regenerate all leaf certificates signed by the new CA.
3. **Redeploy:** BOSH deploys with both CAs trusted and new leaf certs.
4. **Remove old CA:** Remove the `transitional` flag from the new CA and delete the old CA.
5. **Redeploy again:** Final deploy with only the new CA.

### 7.2 Implementation Requirements

- All certificate variables must use the `type: certificate` BOSH variable type (not `type: rsa`).
- All CA references must use the `ca:` option (not inline CA strings).
- Job templates must read CA, cert, and key from BOSH properties (not hardcoded paths).
- The tile's Ops Manager metadata must declare certificate properties with `configurable: false` so they are managed by BOSH, not by the operator manually.

### 7.3 Ops Manager Integration

In the tile metadata, declare the CA as a managed certificate:

```yaml
property_blueprints:
  - name: .properties.seaweedfs_ca
    type: rsa_cert_credentials
    configurable: false
    optional: false
```

This allows Ops Manager's built-in certificate rotation workflow to manage the CA lifecycle.

---

## 8. Backup and Restore

### 8.1 Job: `backup-agent`

**Colocation:** On all volume server and filer VMs.

**Job spec:**

```yaml
# jobs/backup-agent/spec
name: backup-agent

templates:
  bpm.yml.erb: config/bpm.yml
  backup.sh.erb: bin/backup.sh
  restore.sh.erb: bin/restore.sh
  cron.erb: config/cron

packages:
  - seaweedfs
  - backup-tools

consumes:
  - name: master
    type: master

properties:
  backup.enabled:
    description: "Enable scheduled backups"
    default: false
  backup.schedule:
    description: "Cron expression for backup schedule"
    default: "0 2 * * *"
  backup.destination_type:
    description: "Backup destination: local, nfs, s3"
    default: local
  backup.local_path:
    description: "Local backup directory path"
    default: /var/vcap/store/backups
  backup.nfs_endpoint:
    description: "NFS server endpoint (host:/path)"
    default: ""
  backup.s3_endpoint:
    description: "S3-compatible endpoint for remote backup"
    default: ""
  backup.s3_bucket:
    description: "S3 bucket for remote backup"
    default: ""
  backup.s3_access_key:
    description: "S3 access key for backup destination"
    default: ""
  backup.s3_secret_key:
    description: "S3 secret key for backup destination"
    default: ""
  backup.retention_count:
    description: "Number of backups to retain"
    default: 7
```

### 8.2 Backup Script

```bash
#!/bin/bash
# jobs/backup-agent/templates/backup.sh.erb
set -euo pipefail

<% master = link('master') %>
MASTER_URL="<%= master.instances[0].address %>:<%= master.p('port') %>"
TIMESTAMP=$(date +%Y%m%dT%H%M%S)
DEPLOYMENT="<%= spec.deployment %>"
BACKUP_DIR="<%= p('backup.local_path') %>/${DEPLOYMENT}/${TIMESTAMP}"

mkdir -p "${BACKUP_DIR}"

echo "[$(date)] Starting backup for deployment ${DEPLOYMENT}"

# 1. Volume data backup
echo "[$(date)] Backing up volumes..."
/var/vcap/packages/seaweedfs/weed shell -master="${MASTER_URL}" \
  -filer="<%= spec.address %>:8888" <<EOF
lock
volume.list
volume.backup -target="${BACKUP_DIR}/volumes/"
unlock
EOF

# 2. Filer metadata backup
echo "[$(date)] Backing up filer metadata..."
/var/vcap/packages/seaweedfs/weed filer.backup \
  -filer="<%= spec.address %>:8888" \
  -target="${BACKUP_DIR}/filer-meta/"

# 3. IAM configuration export
echo "[$(date)] Backing up IAM configuration..."
curl -s "http://<%= spec.address %>:8333/iam/api/v1/users" \
  > "${BACKUP_DIR}/iam-users.json" || true
curl -s "http://<%= spec.address %>:8333/iam/api/v1/policies" \
  > "${BACKUP_DIR}/iam-policies.json" || true

# 4. Ship to remote destination if configured
<% if p('backup.destination_type') == 's3' %>
echo "[$(date)] Uploading backup to S3..."
aws s3 sync "${BACKUP_DIR}" \
  "s3://<%= p('backup.s3_bucket') %>/${DEPLOYMENT}/${TIMESTAMP}/" \
  --endpoint-url "<%= p('backup.s3_endpoint') %>"
<% elsif p('backup.destination_type') == 'nfs' %>
echo "[$(date)] Copying backup to NFS..."
cp -r "${BACKUP_DIR}" /var/vcap/store/nfs-backup/
<% end %>

# 5. Retention: prune old backups
echo "[$(date)] Pruning backups beyond retention count (<%= p('backup.retention_count') %>)..."
ls -1dt <%= p('backup.local_path') %>/${DEPLOYMENT}/*/ 2>/dev/null | \
  tail -n +$(( <%= p('backup.retention_count') %> + 1 )) | \
  xargs rm -rf || true

echo "[$(date)] Backup complete: ${BACKUP_DIR}"
```

### 8.3 Restore Errand

Create a `restore` errand that accepts a backup timestamp and restores from it:

```yaml
# jobs/restore/spec
name: restore
templates:
  run.erb: bin/run
properties:
  restore.backup_timestamp:
    description: "Timestamp of backup to restore (YYYYMMDDTHHMMSS)"
  restore.source_type:
    description: "Where to pull the backup from: local, nfs, s3"
    default: local
```

The restore script reverses the backup process: stops SeaweedFS services, restores volume .dat/.idx files, restores filer metadata, restarts services, and re-applies IAM configuration from the JSON export.

---

## 9. Smoke Test Expansion

### 9.1 Test Application

Build a small, vendored test application (Go recommended for single-binary simplicity) that exposes HTTP endpoints for S3 operations. The app reads credentials from `VCAP_SERVICES`.

```go
// src/smoke-test-app/main.go (simplified)
package main

import (
    "github.com/aws/aws-sdk-go/aws"
    "github.com/aws/aws-sdk-go/aws/credentials"
    "github.com/aws/aws-sdk-go/aws/session"
    "github.com/aws/aws-sdk-go/service/s3"
)

// HTTP handlers that perform S3 operations using VCAP_SERVICES credentials
// /put - upload test object
// /get - download and verify object
// /multipart - multipart upload > 5MB
// /list - list bucket contents
// /delete - delete test object
// /verify-tls - confirm HTTPS and valid cert
```

Vendor all Go dependencies (`go mod vendor`) for air-gap.

### 9.2 New Test Scenarios

Add the following to the existing `smoke-tests` errand script:

**Shared plan additions:**

```bash
# Push test app and bind
cf push smoke-test-app -p /var/vcap/packages/smoke-test-app/ -b binary_buildpack --no-start
cf create-service seaweedfs shared smoke-test-bucket
cf bind-service smoke-test-app smoke-test-bucket
cf start smoke-test-app

# Validate VCAP_SERVICES
curl https://smoke-test-app.<domain>/verify-credentials

# S3 operations via the bound app
curl https://smoke-test-app.<domain>/put
curl https://smoke-test-app.<domain>/get          # SHA-256 content verification
curl https://smoke-test-app.<domain>/multipart     # >5MB multipart upload
curl https://smoke-test-app.<domain>/list
curl https://smoke-test-app.<domain>/verify-tls    # Confirm HTTPS + valid cert

# Credential revocation test
cf unbind-service smoke-test-app smoke-test-bucket
curl https://smoke-test-app.<domain>/get           # Expect 403

# Cleanup
cf delete-service -f smoke-test-bucket
cf delete -f smoke-test-app
# Verify bucket no longer exists on shared cluster
```

**On-demand plan tests:**

```bash
# Async provisioning
cf create-service seaweedfs dedicated-small smoke-test-cluster
# Poll until ready (timeout: 30 min)
while cf service smoke-test-cluster | grep "in progress"; do sleep 30; done

# Verify BOSH deployment exists
bosh deployments | grep service-instance-

# Push, bind, test (same S3 operations as shared)
cf push smoke-test-app-od ...
cf bind-service smoke-test-app-od smoke-test-cluster
# ... S3 CRUD ...

# On-demand bucket creation
curl -X PUT https://smoke-test-app-od.<domain>/create-bucket?name=extra-bucket

# Deprovision and verify BOSH deployment deleted
cf delete-service -f smoke-test-cluster
# Poll until gone
bosh deployments | grep -v service-instance-
```

---

## 10. Upgrade All Service Instances Errand

### 10.1 Job: `upgrade-all-service-instances`

```yaml
# jobs/upgrade-all-service-instances/spec
name: upgrade-all-service-instances

templates:
  run.erb: bin/run

consumes:
  - name: broker
    type: seaweedfs-broker

properties:
  upgrade.canaries:
    description: "Number of canary instances to upgrade first"
    default: 1
  upgrade.max_in_flight:
    description: "Maximum parallel upgrades after canaries"
    default: 3
  upgrade.fail_fast:
    description: "Stop on first failure (true) or continue and report (false)"
    default: true
```

### 10.2 Errand Script

```bash
#!/bin/bash
# jobs/upgrade-all-service-instances/templates/run.erb
set -euo pipefail

<% broker = link('broker') %>
BROKER_URL="http://<%= broker.instances[0].address %>:<%= broker.p('port') %>"
CANARIES=<%= p('upgrade.canaries') %>
MAX_IN_FLIGHT=<%= p('upgrade.max_in_flight') %>
FAIL_FAST=<%= p('upgrade.fail_fast') %>

# 1. Query broker for all on-demand deployments
DEPLOYMENTS=$(curl -s "${BROKER_URL}/admin/deployments" | jq -r '.[]')
TOTAL=$(echo "${DEPLOYMENTS}" | wc -l)
echo "Found ${TOTAL} on-demand deployments to upgrade"

FAILED=0
UPGRADED=0

upgrade_deployment() {
  local deployment=$1
  echo "[$(date)] Upgrading ${deployment}..."
  bosh -d "${deployment}" deploy /var/vcap/jobs/upgrade-all-service-instances/config/on-demand-manifest.yml \
    --no-redact -n 2>&1 | tee "/tmp/upgrade-${deployment}.log"
  if [ $? -eq 0 ]; then
    echo "[$(date)] Successfully upgraded ${deployment}"
    UPGRADED=$((UPGRADED + 1))
  else
    echo "[$(date)] FAILED to upgrade ${deployment}"
    FAILED=$((FAILED + 1))
    if [ "${FAIL_FAST}" = "true" ]; then
      echo "Fail-fast enabled. Aborting remaining upgrades."
      exit 1
    fi
  fi
}

# 2. Canary phase
echo "=== Canary phase: upgrading ${CANARIES} instance(s) ==="
CANARY_LIST=$(echo "${DEPLOYMENTS}" | head -n ${CANARIES})
for dep in ${CANARY_LIST}; do
  upgrade_deployment "${dep}"
done

# 3. Remaining instances in parallel batches
echo "=== Batch phase: max ${MAX_IN_FLIGHT} in flight ==="
REMAINING=$(echo "${DEPLOYMENTS}" | tail -n +$((CANARIES + 1)))
echo "${REMAINING}" | xargs -P ${MAX_IN_FLIGHT} -I {} bash -c 'upgrade_deployment "$@"' _ {}

# 4. Report
echo "=== Upgrade complete: ${UPGRADED} succeeded, ${FAILED} failed out of ${TOTAL} ==="
[ ${FAILED} -eq 0 ] || exit 1
```

### 10.3 Triggering

Configure in the tile errand settings:

```yaml
# tile.yml errand config
post_deploy_errands:
  - name: upgrade-all-service-instances
    label: Upgrade All On-Demand Instances
    run_default: off    # Manual trigger by default; operator can set to 'on'
```

---

## 11. Tile Configuration Forms

Add the following forms to `tile/metadata/` for the features in this spec.

### 11.1 Syslog Form

```yaml
# tile/metadata/syslog.yml
- name: syslog_config
  label: Syslog
  description: Configure syslog forwarding for all SeaweedFS VMs
  properties:
    - name: syslog_address
      type: string
      label: Syslog Address
      description: "host:port of syslog drain"
      optional: true
    - name: syslog_transport
      type: dropdown_select
      label: Transport Protocol
      default: tcp
      options:
        - name: tcp
          label: TCP
        - name: udp
          label: UDP
        - name: relp
          label: RELP
    - name: syslog_tls_enabled
      type: boolean
      label: Enable TLS
      default: false
    - name: syslog_ca_cert
      type: text
      label: TLS CA Certificate
      optional: true
    - name: syslog_permitted_peer
      type: string
      label: Permitted Peer
      optional: true
```

### 11.2 Observability / OTel Form

```yaml
# tile/metadata/observability.yml
- name: otel_config
  label: Observability
  description: Configure OpenTelemetry metrics export
  properties:
    - name: otlp_endpoint
      type: string
      label: OTLP Endpoint URL
      optional: true
    - name: otlp_protocol
      type: dropdown_select
      label: OTLP Protocol
      default: grpc
      options:
        - name: grpc
          label: gRPC
        - name: http
          label: HTTP
    - name: otlp_ca_cert
      type: text
      label: OTLP TLS CA Certificate
      optional: true
    - name: otel_scrape_interval
      type: string
      label: Metrics Scrape Interval
      default: "30s"
    - name: otel_host_metrics
      type: boolean
      label: Enable Host Metrics
      default: true
```

### 11.3 Backup Form

```yaml
# tile/metadata/backup.yml
- name: backup_config
  label: Backup
  description: Configure backup for all SeaweedFS clusters
  properties:
    - name: backup_enabled
      type: boolean
      label: Enable Scheduled Backups
      default: false
    - name: backup_schedule
      type: string
      label: Backup Schedule (cron)
      default: "0 2 * * *"
    - name: backup_destination_type
      type: dropdown_select
      label: Backup Destination
      default: local
      options:
        - name: local
          label: Local Persistent Disk
        - name: nfs
          label: NFS
        - name: s3
          label: S3-Compatible Store
    - name: backup_s3_endpoint
      type: string
      label: Backup S3 Endpoint
      optional: true
    - name: backup_s3_bucket
      type: string
      label: Backup S3 Bucket
      optional: true
    - name: backup_s3_access_key
      type: secret
      label: Backup S3 Access Key
      optional: true
    - name: backup_s3_secret_key
      type: secret
      label: Backup S3 Secret Key
      optional: true
    - name: backup_retention_count
      type: integer
      label: Backups to Retain
      default: 7
```

---

## 12. Air-Gap Vendoring Completion

The following artifacts need to be vendored into the BOSH release blobs for complete air-gap support.

| Artifact | Action | Size Estimate |
|----------|--------|---------------|
| OTel Collector binary (`otelcol-contrib`) | `bosh add-blob` to `blobs/otel-collector/` | ~100 MB |
| PostgreSQL client (`libpq`, `psql`) | `bosh add-blob` to `blobs/postgresql-client/` | ~10 MB |
| Go modules for broker | `go mod vendor` in `src/service-broker/` | ~50 MB |
| Go modules for smoke test app | `go mod vendor` in `src/smoke-test-app/` | ~30 MB |
| CredHub Go client library | Included in broker vendor directory | (included) |

**Packaging scripts** must set:

```bash
export GOFLAGS="-mod=vendor"
export GONOSUMDB="*"
export GOFLAGS="-mod=vendor"
export GOPROXY="off"
```

---

## 13. CI/CD Pipeline

### 13.1 Stage 1: Upstream Watch (`01-upstream-watch.yml`)

```yaml
name: Upstream Watch
on:
  schedule:
    - cron: '0 */6 * * *'
  workflow_dispatch:

jobs:
  check-upstream:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<pinned-sha>
      - name: Detect new SeaweedFS release
        id: detect
        run: |
          CURRENT=$(yq '.upstream.current' config/versions.yml)
          LATEST=$(gh api repos/seaweedfs/seaweedfs/releases/latest --jq '.tag_name')
          if [ "$CURRENT" != "$LATEST" ]; then
            echo "new_version=${LATEST}" >> $GITHUB_OUTPUT
            echo "has_new=true" >> $GITHUB_OUTPUT
          fi
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Download and verify assets
        if: steps.detect.outputs.has_new == 'true'
        run: ./scripts/fetch-upstream.sh ${{ steps.detect.outputs.new_version }}

      - name: Stage artifacts
        if: steps.detect.outputs.has_new == 'true'
        uses: actions/upload-artifact@<pinned-sha>
        with:
          name: staged-upstream-${{ steps.detect.outputs.new_version }}
          path: staged-artifacts/

      - name: Dispatch to Stage 2
        if: steps.detect.outputs.has_new == 'true'
        uses: peter-evans/repository-dispatch@<pinned-sha>
        with:
          event-type: new-upstream-version
          client-payload: '{"version": "${{ steps.detect.outputs.new_version }}"}'
```

### 13.2 Stage 2: BOSH Release Build (`02-bosh-release-build.yml`)

```yaml
name: BOSH Release Build
on:
  repository_dispatch:
    types: [new-upstream-version]
  workflow_dispatch:

jobs:
  build:
    runs-on: ubuntu-latest  # Use self-hosted for 16GB+ RAM
    steps:
      - uses: actions/checkout@<pinned-sha>
      - name: Download staged artifacts
        uses: actions/download-artifact@<pinned-sha>

      - name: Inject blob
        run: |
          VERSION=${{ github.event.client_payload.version }}
          bosh add-blob staged-artifacts/blobs/seaweedfs-${VERSION}-linux-amd64.tar.gz \
            seaweedfs/seaweedfs-${VERSION}-linux-amd64.tar.gz

      - name: Create dev release
        run: bosh create-release --force --tarball=seaweedfs-dev.tgz

      - name: Start BOSH-lite
        run: ./scripts/start-bosh-lite.sh

      - name: Deploy and smoke test
        run: |
          bosh upload-stemcell <stemcell-url>
          bosh upload-release seaweedfs-dev.tgz
          bosh -d seaweedfs deploy manifests/seaweedfs-single-vm.yml -n
          ./tests/smoke/run.sh seaweedfs

      - name: Create final release
        run: bosh create-release --final --tarball=seaweedfs-release.tgz

      - name: Upload release artifact
        uses: actions/upload-artifact@<pinned-sha>
        with:
          name: bosh-release
          path: seaweedfs-release.tgz

      - name: Dispatch to Stage 3
        uses: peter-evans/repository-dispatch@<pinned-sha>
        with:
          event-type: bosh-release-promoted
```

### 13.3 Stage 3: Tile Package (`03-tile-package.yml`)

```yaml
name: Tile Package
on:
  repository_dispatch:
    types: [bosh-release-promoted]
  workflow_dispatch:

jobs:
  package:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<pinned-sha>
      - name: Download BOSH release
        uses: actions/download-artifact@<pinned-sha>

      - name: Install Kiln
        run: |
          wget -q https://github.com/pivotal-cf/kiln/releases/download/v0.90.0/kiln-linux-amd64
          chmod +x kiln-linux-amd64 && sudo mv kiln-linux-amd64 /usr/local/bin/kiln

      - name: Stage release and bake tile
        run: |
          cp bosh-release/seaweedfs-release.tgz tile/releases/
          kiln bake --metadata tile/tile.yml \
            --releases-directory tile/releases/ \
            --output-file seaweedfs-tile.pivotal

      - name: Validate tile
        run: kiln validate --metadata tile/tile.yml

      - name: Upload tile artifact
        uses: actions/upload-artifact@<pinned-sha>
        with:
          name: tile
          path: seaweedfs-tile.pivotal

      - name: Dispatch to Stage 4
        uses: peter-evans/repository-dispatch@<pinned-sha>
        with:
          event-type: tile-built
```

### 13.4 Stage 4: Tile Test & Publish (`04-tile-test-publish.yml`)

```yaml
name: Tile Test & Publish
on:
  repository_dispatch:
    types: [tile-built]
  workflow_dispatch:

jobs:
  test-and-publish:
    runs-on: ubuntu-latest
    environment: production  # Requires manual approval
    steps:
      - uses: actions/checkout@<pinned-sha>
      - name: Download tile
        uses: actions/download-artifact@<pinned-sha>

      - name: Deploy to Ops Manager
        run: |
          om upload-product -p tile/seaweedfs-tile.pivotal
          om stage-product -p seaweedfs
          om configure-product -c config/tile-config.yml
          om apply-changes --product-name seaweedfs

      - name: Run integration tests
        run: ./tests/integration/run.sh

      - name: Create GitHub Release
        uses: softprops/action-gh-release@<pinned-sha>
        with:
          files: tile/seaweedfs-tile.pivotal
          tag_name: v${{ env.TILE_VERSION }}
```

### 13.5 Dependency Tracker (`dependency-tracker.yml`)

Weekly cron that checks for updates to SeaweedFS, Go, OTel Collector, bpm-release, routing-release, and stemcell. Opens PRs when new versions are found.

### 13.6 Version State File

```yaml
# config/versions.yml (auto-managed)
upstream:
  current: "4.07"
  previous: "4.06"
  detected_at: "2026-02-10T08:00:00Z"
bosh_release:
  current: "1.0.7"
  tarball_sha256: "..."
tile:
  current: "1.0.7-build.1"
  pivotal_sha256: "..."
```

---

## 14. Supply Chain Security

| Requirement | Implementation |
|-------------|----------------|
| Checksum verification | Compute SHA-256 of all downloaded upstream assets; fail pipeline on mismatch |
| Pinned Actions | All GitHub Actions referenced by SHA, not tag (e.g., `actions/checkout@abc123`) |
| Artifact attestations | Use `actions/attest-build-provenance@v1` in build workflows |
| Secret scoping | `PIVNET_TOKEN` scoped to `production` environment; `BOSH_CLIENT_SECRET` scoped to `staging` |
| OIDC auth | Use OIDC for cloud provider credentials instead of static keys where possible |

---

## 15. Implementation Phases

| Phase | Scope | Duration | Dependencies |
|-------|-------|----------|--------------|
| **1: Foundation** | BPM, BOSH links, rack-to-AZ, S3 consolidation (optional) | 2 weeks | None |
| **2: Security** | CredHub integration, mTLS, certificate rotation | 2 weeks | Phase 1 |
| **3: Observability** | syslog-forwarder, otel-collector, tile forms | 1.5 weeks | Phase 1 |
| **4: Backup/Restore** | backup-agent, scheduled backups, restore errand, tile form | 2 weeks | Phase 2 (CredHub for backup keys) |
| **5: Test Expansion** | Vendored test app, shared/on-demand tests, upgrade errand | 2 weeks | Phases 1–4 |
| **6: CI/CD Pipeline** | 4-stage pipeline, dependency tracker, versioning | 3 weeks | Phase 5 (tests run in Stage 2/4) |
| **7: Hardening** | Air-gap validation, supply chain security, perf testing | 1.5 weeks | All phases |

**Total: ~14 weeks** with a dedicated engineer building on the existing foundation.
