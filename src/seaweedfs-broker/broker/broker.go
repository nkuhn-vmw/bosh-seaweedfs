package broker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/cloudfoundry/seaweedfs-broker/bosh"
	"github.com/cloudfoundry/seaweedfs-broker/config"
	"github.com/cloudfoundry/seaweedfs-broker/iam"
	"github.com/cloudfoundry/seaweedfs-broker/store"
)

const (
	// OSB API version
	OSBAPIVersion = "2.17"

	// Plan types
	PlanTypeShared    = "shared"
	PlanTypeDedicated = "dedicated"
)

// Broker implements the Open Service Broker API
type Broker struct {
	config     *config.Config
	store      store.Store
	boshClient *bosh.Client
	s3Client   *minio.Client
	iamClient  *iam.Client
}

// New creates a new broker instance
func New(cfg *config.Config) (*Broker, error) {
	// Initialize state store
	stateStore, err := store.NewFileStore(cfg.StateStore.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	b := &Broker{
		config: cfg,
		store:  stateStore,
	}

	// Initialize BOSH client if configured
	if cfg.BOSH.URL != "" {
		boshClient, err := bosh.NewClient(&cfg.BOSH)
		if err != nil {
			return nil, fmt.Errorf("failed to create BOSH client: %w", err)
		}
		b.boshClient = boshClient
	}

	// Initialize S3 client for shared cluster
	if cfg.SharedCluster.S3Endpoint != "" {
		s3Client, err := minio.New(cfg.SharedCluster.S3Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.SharedCluster.AccessKey, cfg.SharedCluster.SecretKey, ""),
			Secure: cfg.SharedCluster.UseSSL,
			Region: cfg.SharedCluster.Region,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create S3 client: %w", err)
		}
		b.s3Client = s3Client

		// Initialize IAM client for dynamic credential management
		// Use the internal IAM endpoint (direct BOSH connection) to bypass gorouter
		// which may interfere with IAM API requests
		iamEndpoint := cfg.SharedCluster.IAMEndpoint
		if iamEndpoint == "" {
			// Fall back to S3 endpoint if IAM endpoint not configured
			iamEndpoint = cfg.SharedCluster.S3Endpoint
		}
		// Internal BOSH endpoints don't use SSL
		iamUseSSL := false
		if iamEndpoint == cfg.SharedCluster.S3Endpoint {
			iamUseSSL = cfg.SharedCluster.UseSSL
		}
		log.Printf("Initializing IAM client with endpoint: %s (SSL: %v)", iamEndpoint, iamUseSSL)
		b.iamClient = iam.NewClient(
			iamEndpoint,
			cfg.SharedCluster.AccessKey,
			cfg.SharedCluster.SecretKey,
			cfg.SharedCluster.Region,
			iamUseSSL,
		)
	}

	return b, nil
}

// Router returns the HTTP router for the broker
func (b *Broker) Router() http.Handler {
	r := mux.NewRouter()

	// Health check endpoint (no auth required)
	r.HandleFunc("/health", b.healthHandler).Methods("GET")

	// Icon endpoint (no auth required) - serves marketplace icon
	r.HandleFunc("/icon.png", b.iconHandler).Methods("GET")

	// OSB API endpoints
	api := r.PathPrefix("/v2").Subrouter()
	api.Use(b.authMiddleware)
	api.Use(b.osbVersionMiddleware)

	api.HandleFunc("/catalog", b.catalogHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}", b.provisionHandler).Methods("PUT")
	api.HandleFunc("/service_instances/{instance_id}", b.deprovisionHandler).Methods("DELETE")
	api.HandleFunc("/service_instances/{instance_id}", b.getInstanceHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}/last_operation", b.lastOperationHandler).Methods("GET")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.bindHandler).Methods("PUT")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.unbindHandler).Methods("DELETE")
	api.HandleFunc("/service_instances/{instance_id}/service_bindings/{binding_id}", b.getBindingHandler).Methods("GET")

	return r
}

// Middleware

func (b *Broker) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != b.config.Auth.Username || password != b.config.Auth.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="SeaweedFS Service Broker"`)
			b.writeError(w, http.StatusUnauthorized, "Unauthorized", "Invalid or missing credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *Broker) osbVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("X-Broker-API-Version")
		if version == "" {
			b.writeError(w, http.StatusPreconditionFailed, "MissingAPIVersion",
				"X-Broker-API-Version header is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler implementations

func (b *Broker) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (b *Broker) iconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write(iconPNG)
}

func (b *Broker) catalogHandler(w http.ResponseWriter, r *http.Request) {
	catalog := b.buildCatalog()
	b.writeJSON(w, http.StatusOK, catalog)
}

func (b *Broker) provisionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	// Check if instance already exists
	existing, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if existing != nil {
		// Return 200 if same parameters, 409 if different
		b.writeJSON(w, http.StatusOK, map[string]any{
			"dashboard_url": b.getDashboardURL(existing),
		})
		return
	}

	// Parse request
	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		b.writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON body")
		return
	}

	// Validate service and plan
	plan := b.findPlan(req.ServiceID, req.PlanID)
	if plan == nil {
		b.writeError(w, http.StatusBadRequest, "InvalidPlan", "Unknown service or plan ID")
		return
	}

	// Check async requirement for dedicated plans
	acceptsAsync := r.URL.Query().Get("accepts_incomplete") == "true"
	if plan.PlanType == PlanTypeDedicated && !acceptsAsync {
		b.writeError(w, http.StatusUnprocessableEntity, "AsyncRequired",
			"This plan requires asynchronous provisioning")
		return
	}

	// Create instance
	instance := &store.ServiceInstance{
		ID:               instanceID,
		ServiceID:        req.ServiceID,
		PlanID:           req.PlanID,
		OrganizationGUID: req.OrganizationGUID,
		SpaceGUID:        req.SpaceGUID,
		Parameters:       req.Parameters,
		Context:          req.Context,
		CreatedAt:        time.Now(),
		State:            "provisioning",
	}

	if plan.PlanType == PlanTypeShared {
		// Provision shared bucket synchronously
		if err := b.provisionSharedBucket(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "ProvisionError", err.Error())
			return
		}
		instance.State = "succeeded"
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		b.writeJSON(w, http.StatusCreated, map[string]any{
			"dashboard_url": b.getDashboardURL(instance),
		})
	} else {
		// Provision dedicated cluster asynchronously
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		go b.provisionDedicatedCluster(instance, plan)
		b.writeJSON(w, http.StatusAccepted, map[string]any{
			"dashboard_url": b.getDashboardURL(instance),
			"operation":     "provision",
		})
	}
}

func (b *Broker) deprovisionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	plan := b.findPlan(instance.ServiceID, instance.PlanID)
	acceptsAsync := r.URL.Query().Get("accepts_incomplete") == "true"

	// Check for existing bindings
	bindings, err := b.store.ListBindingsForInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if len(bindings) > 0 {
		b.writeError(w, http.StatusBadRequest, "BindingsExist",
			"Cannot deprovision instance with active bindings")
		return
	}

	if plan != nil && plan.PlanType == PlanTypeDedicated {
		if !acceptsAsync {
			b.writeError(w, http.StatusUnprocessableEntity, "AsyncRequired",
				"This plan requires asynchronous deprovisioning")
			return
		}
		instance.State = "deprovisioning"
		if err := b.store.SaveInstance(instance); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		go b.deprovisionDedicatedCluster(instance)
		b.writeJSON(w, http.StatusAccepted, map[string]any{
			"operation": "deprovision",
		})
	} else {
		// Deprovision shared bucket synchronously
		if err := b.deprovisionSharedBucket(instance); err != nil {
			log.Printf("Warning: failed to delete bucket: %v", err)
		}
		if err := b.store.DeleteInstance(instanceID); err != nil {
			b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
			return
		}
		b.writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func (b *Broker) getInstanceHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	b.writeJSON(w, http.StatusOK, map[string]any{
		"service_id":    instance.ServiceID,
		"plan_id":       instance.PlanID,
		"dashboard_url": b.getDashboardURL(instance),
		"parameters":    instance.Parameters,
	})
}

func (b *Broker) lastOperationHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	state := "in progress"
	switch instance.State {
	case "succeeded":
		state = "succeeded"
	case "failed":
		state = "failed"
	}

	response := map[string]any{
		"state": state,
	}
	if instance.StateMessage != "" {
		response["description"] = instance.StateMessage
	}

	b.writeJSON(w, http.StatusOK, response)
}

func (b *Broker) bindHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	// Check if instance is ready
	if instance.State != "succeeded" {
		b.writeError(w, http.StatusUnprocessableEntity, "InstanceNotReady",
			"Service instance is not ready")
		return
	}

	// Check if binding already exists
	existing, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if existing != nil {
		b.writeJSON(w, http.StatusOK, b.buildCredentials(instance, existing))
		return
	}

	// Parse request
	var req BindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		b.writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON body")
		return
	}

	binding := &store.ServiceBinding{
		ID:         bindingID,
		InstanceID: instanceID,
		AppGUID:    req.AppGUID,
		Parameters: req.Parameters,
		CreatedAt:  time.Now(),
	}

	// For dedicated clusters, ensure the bucket exists before creating credentials
	if instance.DeploymentName != "" && instance.IAMEndpoint != "" && instance.BucketName != "" {
		dedicatedS3, err := minio.New(instance.IAMEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(instance.AdminAccessKey, instance.AdminSecretKey, ""),
			Secure: false,
			Region: b.config.SharedCluster.Region,
		})
		if err != nil {
			log.Printf("Binding %s: Warning: could not create S3 client for bucket check: %v", bindingID, err)
		} else {
			ctx := context.Background()
			exists, err := dedicatedS3.BucketExists(ctx, instance.BucketName)
			if err != nil {
				log.Printf("Binding %s: Warning: could not check bucket existence: %v", bindingID, err)
			} else if !exists {
				log.Printf("Binding %s: Creating bucket %s on dedicated cluster", bindingID, instance.BucketName)
				if err := dedicatedS3.MakeBucket(ctx, instance.BucketName, minio.MakeBucketOptions{
					Region: b.config.SharedCluster.Region,
				}); err != nil {
					log.Printf("Binding %s: Warning: could not create bucket: %v", bindingID, err)
				}
			}
		}
	}

	// Create per-binding IAM credentials (for both shared and dedicated clusters)
	if err := b.createS3Credentials(instance, binding); err != nil {
		b.writeError(w, http.StatusInternalServerError, "BindError", err.Error())
		return
	}

	if err := b.store.SaveBinding(binding); err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}

	b.writeJSON(w, http.StatusCreated, b.buildCredentials(instance, binding))
}

func (b *Broker) unbindHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusGone, "InstanceNotFound", "Service instance not found")
		return
	}

	binding, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if binding == nil {
		b.writeError(w, http.StatusGone, "BindingNotFound", "Service binding not found")
		return
	}

	// Revoke S3 credentials
	if err := b.deleteS3Credentials(instance, binding); err != nil {
		log.Printf("Warning: failed to delete S3 credentials: %v", err)
	}

	if err := b.store.DeleteBinding(bindingID); err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}

	b.writeJSON(w, http.StatusOK, map[string]any{})
}

func (b *Broker) getBindingHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	instanceID := vars["instance_id"]
	bindingID := vars["binding_id"]

	instance, err := b.store.GetInstance(instanceID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if instance == nil {
		b.writeError(w, http.StatusNotFound, "InstanceNotFound", "Service instance not found")
		return
	}

	binding, err := b.store.GetBinding(bindingID)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "StoreError", err.Error())
		return
	}
	if binding == nil {
		b.writeError(w, http.StatusNotFound, "BindingNotFound", "Service binding not found")
		return
	}

	b.writeJSON(w, http.StatusOK, b.buildCredentials(instance, binding))
}

// Helper methods

func (b *Broker) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (b *Broker) writeError(w http.ResponseWriter, status int, errorCode, description string) {
	b.writeJSON(w, status, map[string]string{
		"error":       errorCode,
		"description": description,
	})
}

func (b *Broker) findPlan(serviceID, planID string) *config.PlanConfig {
	for _, service := range b.config.Catalog.Services {
		if service.ID == serviceID {
			for _, plan := range service.Plans {
				if plan.ID == planID {
					return &plan
				}
			}
		}
	}
	return nil
}

func (b *Broker) getDashboardURL(instance *store.ServiceInstance) string {
	// Return appropriate dashboard URL based on instance type
	if instance.DeploymentName != "" && instance.ConsoleURL != "" {
		return instance.ConsoleURL
	}
	if instance.DeploymentName != "" {
		// Generate console URL if CF domain is configured
		if b.config.CF.SystemDomain != "" {
			return fmt.Sprintf("https://seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
		}
	}
	return ""
}

func (b *Broker) buildCatalog() map[string]any {
	services := make([]map[string]any, 0, len(b.config.Catalog.Services))

	for _, svc := range b.config.Catalog.Services {
		plans := make([]map[string]any, 0, len(svc.Plans))
		for _, plan := range svc.Plans {
			planData := map[string]any{
				"id":          plan.ID,
				"name":        plan.Name,
				"description": plan.Description,
				"free":        plan.Free,
				"bindable":    true,
				"metadata": map[string]any{
					"displayName": plan.Metadata.DisplayName,
					"bullets":     plan.Metadata.Bullets,
				},
			}
			plans = append(plans, planData)
		}

		// Use embedded icon as data URI if no external imageUrl configured
		imageURL := svc.Metadata.ImageURL
		if imageURL == "" && len(iconPNG) > 0 {
			imageURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(iconPNG)
		}

		serviceData := map[string]any{
			"id":                    svc.ID,
			"name":                  svc.Name,
			"description":           svc.Description,
			"bindable":              svc.Bindable,
			"instances_retrievable": true,
			"bindings_retrievable":  true,
			"plan_updateable":       false,
			"plans":                 plans,
			"tags":                  svc.Tags,
			"metadata": map[string]any{
				"displayName":         svc.Metadata.DisplayName,
				"imageUrl":            imageURL,
				"longDescription":     svc.Metadata.LongDescription,
				"providerDisplayName": svc.Metadata.ProviderDisplayName,
				"documentationUrl":    svc.Metadata.DocumentationURL,
				"supportUrl":          svc.Metadata.SupportURL,
			},
		}

		services = append(services, serviceData)
	}

	return map[string]any{
		"services": services,
	}
}

func (b *Broker) buildCredentials(instance *store.ServiceInstance, binding *store.ServiceBinding) map[string]any {
	var endpoint, bucket string
	var useSSL bool

	if instance.DeploymentName != "" {
		// Dedicated cluster - direct IP:port has no TLS, gorouter hostnames do
		endpoint = instance.S3Endpoint
		bucket = instance.BucketName
		useSSL = !strings.Contains(endpoint, ":8333")
		if endpoint == "" {
			log.Printf("WARNING: Building credentials for dedicated cluster %s but S3Endpoint is empty!", instance.DeploymentName)
		}
	} else {
		// Shared cluster
		endpoint = b.config.SharedCluster.S3Endpoint
		bucket = instance.BucketName
		useSSL = b.config.SharedCluster.UseSSL
	}

	// Always use the binding's credentials - they are created dynamically via IAM API
	// for shared clusters, or configured in the deployment manifest for dedicated clusters
	accessKey := binding.AccessKey
	secretKey := binding.SecretKey

	// Construct endpoint URL with appropriate protocol
	protocol := "http"
	if useSSL {
		protocol = "https"
	}
	endpointURL := fmt.Sprintf("%s://%s", protocol, endpoint)

	creds := map[string]any{
		"endpoint":     endpoint,
		"endpoint_url": endpointURL,
		"bucket":       bucket,
		"access_key":   accessKey,
		"secret_key":   secretKey,
		"region":       b.config.SharedCluster.Region,
		"use_ssl":      useSSL,
		"uri":          fmt.Sprintf("s3://%s:%s@%s/%s", accessKey, secretKey, endpoint, bucket),
	}

	// Include console URL for dedicated clusters
	if instance.DeploymentName != "" && instance.ConsoleURL != "" {
		creds["console_url"] = instance.ConsoleURL
	}

	return map[string]any{
		"credentials": creds,
	}
}

func generateAccessKey() string {
	bytes := make([]byte, 10)
	rand.Read(bytes)
	return strings.ToUpper(hex.EncodeToString(bytes))
}

func generateSecretKey() string {
	bytes := make([]byte, 20)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func getMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Request/Response types

type ProvisionRequest struct {
	ServiceID        string         `json:"service_id"`
	PlanID           string         `json:"plan_id"`
	OrganizationGUID string         `json:"organization_guid"`
	SpaceGUID        string         `json:"space_guid"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	Context          map[string]any `json:"context,omitempty"`
}

type BindRequest struct {
	ServiceID  string         `json:"service_id"`
	PlanID     string         `json:"plan_id"`
	AppGUID    string         `json:"app_guid,omitempty"`
	BindResource map[string]any `json:"bind_resource,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Provisioning implementations

func (b *Broker) provisionSharedBucket(instance *store.ServiceInstance) error {
	if b.s3Client == nil {
		return fmt.Errorf("shared cluster not configured")
	}

	// Generate bucket name
	bucketName := fmt.Sprintf("cf-%s-%s", instance.SpaceGUID[:8], instance.ID[:8])
	instance.BucketName = bucketName

	// Create bucket
	ctx := context.Background()
	err := b.s3Client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{
		Region: b.config.SharedCluster.Region,
	})
	if err != nil {
		// Check if bucket already exists
		exists, errBucketExists := b.s3Client.BucketExists(ctx, bucketName)
		if errBucketExists != nil || !exists {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	log.Printf("Created bucket %s for instance %s", bucketName, instance.ID)
	return nil
}

func (b *Broker) deprovisionSharedBucket(instance *store.ServiceInstance) error {
	if b.s3Client == nil || instance.BucketName == "" {
		return nil
	}

	ctx := context.Background()

	// Delete all objects in bucket first
	objectsCh := b.s3Client.ListObjects(ctx, instance.BucketName, minio.ListObjectsOptions{
		Recursive: true,
	})

	for object := range objectsCh {
		if object.Err != nil {
			log.Printf("Error listing objects: %v", object.Err)
			continue
		}
		err := b.s3Client.RemoveObject(ctx, instance.BucketName, object.Key, minio.RemoveObjectOptions{})
		if err != nil {
			log.Printf("Error removing object %s: %v", object.Key, err)
		}
	}

	// Delete bucket
	if err := b.s3Client.RemoveBucket(ctx, instance.BucketName); err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	log.Printf("Deleted bucket %s for instance %s", instance.BucketName, instance.ID)
	return nil
}

func (b *Broker) createS3Credentials(instance *store.ServiceInstance, binding *store.ServiceBinding) error {
	// Determine which IAM client to use
	var iamClient *iam.Client

	if instance.DeploymentName != "" {
		// Dedicated cluster: create IAM client for the on-demand cluster
		iamEndpoint := instance.IAMEndpoint
		if iamEndpoint == "" {
			// Fall back to admin credentials without per-binding IAM
			log.Printf("Binding %s: No IAM endpoint for dedicated cluster %s, using admin credentials", binding.ID, instance.DeploymentName)
			binding.AccessKey = instance.AdminAccessKey
			binding.SecretKey = instance.AdminSecretKey
			return nil
		}
		iamClient = iam.NewClient(iamEndpoint, instance.AdminAccessKey, instance.AdminSecretKey, b.config.SharedCluster.Region, false)
		log.Printf("Binding %s: Created IAM client for dedicated cluster at %s", binding.ID, iamEndpoint)
	} else {
		// Shared cluster: use the pre-configured IAM client
		if b.iamClient == nil {
			return fmt.Errorf("IAM client not initialized - cannot create per-binding credentials")
		}
		iamClient = b.iamClient
	}

	// Create per-binding IAM credentials (same flow for shared and dedicated)
	userName := fmt.Sprintf("cf-binding-%s", binding.ID[:min(len(binding.ID), 16)])
	log.Printf("Binding %s: Creating IAM user %s via SeaweedFS IAM API", binding.ID, userName)

	if err := iamClient.CreateUser(userName); err != nil {
		log.Printf("Binding %s: IAM CreateUser failed: %v", binding.ID, err)
		return fmt.Errorf("failed to create IAM user: %w", err)
	}

	accessKey, err := iamClient.CreateAccessKey(userName)
	if err != nil {
		log.Printf("Binding %s: IAM CreateAccessKey failed: %v", binding.ID, err)
		return fmt.Errorf("failed to create S3 credentials via IAM API: %w", err)
	}

	binding.IAMUserName = userName
	binding.AccessKey = accessKey.AccessKeyID
	binding.SecretKey = accessKey.SecretAccessKey

	log.Printf("Binding %s: Created IAM credentials for user %s, access_key=%s",
		binding.ID, userName, accessKey.AccessKeyID)

	// Optionally attach a policy to restrict access to only this binding's bucket
	policyName := fmt.Sprintf("bucket-access-%s", binding.ID[:min(len(binding.ID), 8)])
	if err := iamClient.PutUserPolicy(userName, policyName, instance.BucketName); err != nil {
		log.Printf("Warning: Could not attach bucket policy for user %s: %v", userName, err)
	}

	return nil
}

func (b *Broker) deleteS3Credentials(instance *store.ServiceInstance, binding *store.ServiceBinding) error {
	if binding.IAMUserName == "" {
		log.Printf("Binding %s: No IAM credentials to delete", binding.ID)
		return nil
	}

	// Determine which IAM client to use
	var iamClient *iam.Client

	if instance.DeploymentName != "" {
		iamEndpoint := instance.IAMEndpoint
		if iamEndpoint == "" {
			log.Printf("Binding %s: No IAM endpoint for dedicated cluster, skipping credential cleanup", binding.ID)
			return nil
		}
		iamClient = iam.NewClient(iamEndpoint, instance.AdminAccessKey, instance.AdminSecretKey, b.config.SharedCluster.Region, false)
	} else {
		if b.iamClient == nil {
			log.Printf("Binding %s: No IAM client available for cleanup", binding.ID)
			return nil
		}
		iamClient = b.iamClient
	}

	log.Printf("Binding %s: Deleting IAM credentials for user %s", binding.ID, binding.IAMUserName)

	// Delete the user policy first (best effort)
	policyName := fmt.Sprintf("bucket-access-%s", binding.ID[:min(len(binding.ID), 8)])
	if err := iamClient.DeleteUserPolicy(binding.IAMUserName, policyName); err != nil {
		log.Printf("Warning: Could not delete user policy: %v", err)
	}

	if binding.AccessKey != "" {
		if err := iamClient.DeleteAccessKey(binding.IAMUserName, binding.AccessKey); err != nil {
			log.Printf("Warning: Could not delete access key %s: %v", binding.AccessKey, err)
		}
	}

	if err := iamClient.DeleteUser(binding.IAMUserName); err != nil {
		log.Printf("Warning: Could not delete IAM user %s: %v", binding.IAMUserName, err)
	}

	log.Printf("Binding %s: Deleted IAM credentials and user %s", binding.ID, binding.IAMUserName)
	return nil
}

func (b *Broker) provisionDedicatedCluster(instance *store.ServiceInstance, plan *config.PlanConfig) {
	if b.boshClient == nil {
		instance.State = "failed"
		instance.StateMessage = "BOSH director not configured"
		b.store.SaveInstance(instance)
		return
	}

	deploymentName := fmt.Sprintf("%s-%s", b.config.BOSH.DeploymentPrefix, instance.ID[:8])
	instance.DeploymentName = deploymentName

	// Generate admin credentials before manifest so they are included in the deployment
	instance.AdminAccessKey = generateAccessKey()
	instance.AdminSecretKey = generateSecretKey()
	instance.BucketName = "default"

	// Use AZs from plan config (passed from tile's availability_zone_names).
	// Fall back to BOSH cloud config discovery if plan AZs are empty.
	if plan.DedicatedConfig != nil {
		if len(plan.DedicatedConfig.AZs) > 0 {
			log.Printf("Using configured AZs for network %s: %v", plan.DedicatedConfig.Network, plan.DedicatedConfig.AZs)
		} else if plan.DedicatedConfig.Network != "" {
			log.Printf("No AZs configured, attempting to discover from BOSH cloud config for network %s", plan.DedicatedConfig.Network)
			azs, err := b.boshClient.GetCloudConfigAZsForNetwork(plan.DedicatedConfig.Network)
			if err != nil {
				log.Printf("Warning: could not discover AZs from cloud config: %v, using fallback [z1]", err)
				plan.DedicatedConfig.AZs = []string{"z1"}
			} else {
				log.Printf("Discovered AZs for network %s: %v", plan.DedicatedConfig.Network, azs)
				plan.DedicatedConfig.AZs = azs
			}
		}
	}

	// Generate manifest
	manifest := b.generateDedicatedManifest(instance, plan)
	log.Printf("Generated manifest for deployment %s:\n%s", deploymentName, string(manifest))

	// Deploy
	task, err := b.boshClient.Deploy(manifest)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Failed to start deployment: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	instance.StateMessage = fmt.Sprintf("Deployment started, task ID: %d", task.ID)
	b.store.SaveInstance(instance)

	// Wait for deployment
	task, err = b.boshClient.WaitForTask(task.ID, 30*time.Minute)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Deployment failed: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	// Discover S3 VM internal endpoint for IAM operations
	hasCFDeployment := b.config.CF.DeploymentName != "" && b.config.CF.SystemDomain != ""
	vms, err := b.boshClient.GetDeploymentVMs(deploymentName)
	if err != nil {
		log.Printf("Warning: could not get deployment VMs for %s: %v", deploymentName, err)
	} else {
		log.Printf("Got %d VMs for deployment %s", len(vms), deploymentName)
		for i, vm := range vms {
			jobName := ""
			if j, ok := vm["job_name"].(string); ok && j != "" {
				jobName = j
			} else if j, ok := vm["job"].(string); ok && j != "" {
				jobName = j
			} else if inst, ok := vm["instance"].(string); ok && inst != "" {
				if idx := strings.Index(inst, "/"); idx > 0 {
					jobName = inst[:idx]
				}
			}

			if i == 0 {
				log.Printf("VM fields available: %v", getMapKeys(vm))
			}
			log.Printf("VM %d: jobName=%s, instance=%v, dns=%v, ips=%v", i, jobName, vm["instance"], vm["dns"], vm["ips"])

			if jobName == "seaweedfs-s3" {
				// Always capture internal endpoint for IAM operations (IP-based, no TLS)
				if ips, ok := vm["ips"].([]any); ok && len(ips) > 0 {
					instance.IAMEndpoint = fmt.Sprintf("%v:8333", ips[0])
					log.Printf("Set IAMEndpoint from IP: %s", instance.IAMEndpoint)
				}
				// Set S3Endpoint for bindings - prefer DNS for stable addressing
				if dns, ok := vm["dns"].([]any); ok && len(dns) > 0 {
					instance.S3Endpoint = fmt.Sprintf("%v:8333", dns[0])
					log.Printf("Set S3Endpoint from DNS: %s", instance.S3Endpoint)
				} else if ips, ok := vm["ips"].([]any); ok && len(ips) > 0 {
					instance.S3Endpoint = fmt.Sprintf("%v:8333", ips[0])
					log.Printf("Set S3Endpoint from IP: %s", instance.S3Endpoint)
				}
			}
		}
		if instance.S3Endpoint == "" {
			log.Printf("Warning: No seaweedfs-s3 job found in deployment VMs")
		}
	}

	// If route_registrar was configured (requires both CF deployment and NATS config),
	// use the gorouter hostname as the S3 endpoint
	hasNATSConfig := b.config.NATS.TLS.Enabled && b.config.NATS.TLS.ClientCert != ""
	if hasCFDeployment && hasNATSConfig {
		s3RouteHost := fmt.Sprintf("seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
		instance.S3Endpoint = s3RouteHost
		instance.ConsoleURL = fmt.Sprintf("https://%s", s3RouteHost)
		log.Printf("Set S3Endpoint to gorouter route: %s", instance.S3Endpoint)
	}

	// Create the default bucket on the dedicated cluster using admin credentials
	if instance.IAMEndpoint != "" {
		log.Printf("Creating default bucket on dedicated cluster at %s", instance.IAMEndpoint)
		dedicatedS3, err := minio.New(instance.IAMEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(instance.AdminAccessKey, instance.AdminSecretKey, ""),
			Secure: false,
			Region: b.config.SharedCluster.Region,
		})
		if err != nil {
			log.Printf("Warning: could not create S3 client for dedicated cluster: %v", err)
		} else {
			ctx := context.Background()
			err = dedicatedS3.MakeBucket(ctx, instance.BucketName, minio.MakeBucketOptions{
				Region: b.config.SharedCluster.Region,
			})
			if err != nil {
				// Check if bucket already exists
				exists, existErr := dedicatedS3.BucketExists(ctx, instance.BucketName)
				if existErr != nil || !exists {
					log.Printf("Warning: could not create default bucket: %v", err)
				} else {
					log.Printf("Default bucket %s already exists", instance.BucketName)
				}
			} else {
				log.Printf("Created default bucket %s on dedicated cluster", instance.BucketName)
			}
		}
	}

	instance.State = "succeeded"
	instance.StateMessage = "Deployment complete"
	b.store.SaveInstance(instance)

	log.Printf("Provisioned dedicated cluster %s for instance %s", deploymentName, instance.ID)
}

func (b *Broker) deprovisionDedicatedCluster(instance *store.ServiceInstance) {
	if b.boshClient == nil || instance.DeploymentName == "" {
		b.store.DeleteInstance(instance.ID)
		return
	}

	// Check if the deployment actually exists before trying to delete it
	deployment, err := b.boshClient.GetDeployment(instance.DeploymentName)
	if err != nil {
		log.Printf("Warning: could not check deployment %s: %v", instance.DeploymentName, err)
	}

	if deployment == nil {
		// Deployment doesn't exist (never created or already deleted) - just clean up state
		log.Printf("Deployment %s does not exist, cleaning up broker state", instance.DeploymentName)
		b.store.DeleteInstance(instance.ID)
		return
	}

	task, err := b.boshClient.DeleteDeployment(instance.DeploymentName)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Failed to delete deployment: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	_, err = b.boshClient.WaitForTask(task.ID, 15*time.Minute)
	if err != nil {
		instance.State = "failed"
		instance.StateMessage = fmt.Sprintf("Delete deployment failed: %v", err)
		b.store.SaveInstance(instance)
		return
	}

	b.store.DeleteInstance(instance.ID)
	log.Printf("Deprovisioned dedicated cluster %s for instance %s", instance.DeploymentName, instance.ID)
}

func (b *Broker) generateDedicatedManifest(instance *store.ServiceInstance, plan *config.PlanConfig) []byte {
	cfg := plan.DedicatedConfig
	if cfg == nil {
		cfg = &config.DedicatedPlanConfig{
			VMType:      "default",
			DiskType:    "default",
			MasterNodes: 1,
			VolumeNodes: 3,
			FilerNodes:  1,
			Replication: "001",
			Network:     "default",
			AZs:         []string{"z1"},
		}
	}
	if cfg.Replication == "" {
		cfg.Replication = "001"
	}

	// Check if we can set up route registration via property-based NATS config
	hasCFDeployment := b.config.CF.DeploymentName != "" && b.config.CF.SystemDomain != ""
	hasNATSConfig := b.config.NATS.TLS.Enabled && b.config.NATS.TLS.ClientCert != "" && len(b.config.NATS.Machines) > 0
	canRouteRegister := hasCFDeployment && hasNATSConfig

	// Generate the S3 route hostname for this instance
	s3RouteHost := ""
	if canRouteRegister {
		s3RouteHost = fmt.Sprintf("seaweedfs-%s.%s", instance.ID[:8], b.config.CF.SystemDomain)
	}

	// Build releases section
	routingReleaseVersion := b.config.BOSH.RoutingReleaseVersion
	if routingReleaseVersion == "" {
		routingReleaseVersion = "latest"
	}

	releasesSection := fmt.Sprintf(`releases:
- name: %s
  version: "%s"`,
		b.config.BOSH.ReleaseName,
		b.config.BOSH.ReleaseVersion,
	)

	// Add routing and bpm releases if we have NATS config for route registration
	if canRouteRegister {
		releasesSection += fmt.Sprintf(`
- name: routing
  version: "%s"
- name: bpm
  version: "latest"`, routingReleaseVersion)
	}

	// Build the S3 instance group jobs section
	s3JobsSection := fmt.Sprintf(`  jobs:
  - name: seaweedfs-s3
    release: %s
    properties:
      seaweedfs:
        s3:
          iam:
            enabled: true
          config:
            enabled: true
            identities:
            - name: admin
              credentials:
              - accessKey: %s
                secretKey: %s
              actions:
              - Admin
              - Read
              - Write`,
		b.config.BOSH.ReleaseName,
		instance.AdminAccessKey,
		instance.AdminSecretKey,
	)

	// Add route_registrar with property-based NATS config (no cross-deployment links needed)
	if canRouteRegister && s3RouteHost != "" {
		// Use nats.service.cf.internal as the NATS hostname. This Consul-style DNS name:
		// 1. Matches the NATS TLS certificate SANs (nats.service.cf.internal, *.nats.service.cf.internal)
		// 2. Resolves on all VMs via BOSH DNS runtime config aliases
		// 3. Avoids needing BOSH API access or IP addresses (which fail TLS verification)
		natsHost := "nats.service.cf.internal"
		natsPort := b.config.NATS.Port
		if natsPort == 0 {
			natsPort = 4224
		}

		natsMachinesYAML := fmt.Sprintf("\n      - %s", natsHost)

		// Include NATS user/password if available (required for NATS authorization)
		natsUserYAML := ""
		if b.config.NATS.User != "" {
			natsUserYAML = fmt.Sprintf("\n      user: %s\n      password: %s", b.config.NATS.User, b.config.NATS.Password)
		}

		s3JobsSection += fmt.Sprintf(`
  - name: route_registrar
    release: routing
    consumes:
      nats: nil
      nats-tls: nil
  - name: bpm
    release: bpm
  properties:
    nats:
      machines:%s
      port: %d%s
      tls:
        enabled: true
        client_cert: |
%s
        client_key: |
%s
        ca_cert: |
%s
    route_registrar:
      routes:
      - name: seaweedfs-s3-ondemand
        port: 8333
        registration_interval: 20s
        uris:
        - %s`,
			natsMachinesYAML,
			natsPort,
			natsUserYAML,
			formatCertForYAML(b.config.NATS.TLS.ClientCert, 10),
			formatCertForYAML(b.config.NATS.TLS.ClientKey, 10),
			formatCertForYAML(b.config.NATS.TLS.CACert, 10),
			s3RouteHost,
		)
	}

	manifest := fmt.Sprintf(`---
name: %s

%s

stemcells:
- alias: default
  os: %s
  version: "%s"

update:
  canaries: 1
  max_in_flight: 1
  canary_watch_time: 30000-300000
  update_watch_time: 30000-300000

instance_groups:
- name: seaweedfs-master
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-master
    release: %s
    properties:
      seaweedfs:
        master:
          port: 9333
          default_replication: "%s"

- name: seaweedfs-volume
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-volume
    release: %s

- name: seaweedfs-filer
  instances: %d
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
  persistent_disk_type: %s
  jobs:
  - name: seaweedfs-filer
    release: %s

- name: seaweedfs-s3
  instances: 1
  vm_type: %s
  stemcell: default
  azs: %s
  networks:
  - name: %s
%s
`,
		instance.DeploymentName,
		releasesSection,
		b.config.BOSH.StemcellOS,
		b.config.BOSH.StemcellVersion,
		cfg.MasterNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.Replication,
		cfg.VolumeNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.FilerNodes,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		cfg.DiskType,
		b.config.BOSH.ReleaseName,
		cfg.VMType,
		toYAMLArray(cfg.AZs),
		cfg.Network,
		s3JobsSection,
	)

	return []byte(manifest)
}

func toYAMLArray(items []string) string {
	if len(items) == 0 {
		return "[z1]"
	}
	return fmt.Sprintf("[%s]", strings.Join(items, ", "))
}

// formatCertForYAML indents a PEM certificate for embedding in YAML block scalar format.
// Each line of the cert will be prefixed with the specified number of spaces.
func formatCertForYAML(cert string, indent int) string {
	prefix := strings.Repeat(" ", indent)
	lines := strings.Split(strings.TrimSpace(cert), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

