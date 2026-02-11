package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

const testObjectKey = "smoke-test-object"
const testContent = "SeaweedFS smoke test content - the quick brown fox jumps over the lazy dog"

var s3Client *s3.S3
var bucketName string

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if err := initS3Client(); err != nil {
		log.Fatalf("Failed to initialize S3 client: %v", err)
	}

	http.HandleFunc("/", healthHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/verify-credentials", verifyCredentialsHandler)
	http.HandleFunc("/put", putHandler)
	http.HandleFunc("/get", getHandler)
	http.HandleFunc("/multipart", multipartHandler)
	http.HandleFunc("/list", listHandler)
	http.HandleFunc("/delete", deleteHandler)
	http.HandleFunc("/verify-tls", verifyTLSHandler)

	log.Printf("Smoke test app listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

type vcapServices struct {
	SeaweedFS []struct {
		Credentials struct {
			AccessKey string `json:"access_key_id"`
			SecretKey string `json:"secret_access_key"`
			Bucket    string `json:"bucket"`
			Endpoint  string `json:"endpoint"`
			URI       string `json:"uri"`
			UseSSL    bool   `json:"use_ssl"`
			Region    string `json:"region"`
		} `json:"credentials"`
	} `json:"seaweedfs"`
}

func initS3Client() error {
	vcapJSON := os.Getenv("VCAP_SERVICES")
	if vcapJSON == "" {
		return fmt.Errorf("VCAP_SERVICES not set")
	}

	var vcap vcapServices
	if err := json.Unmarshal([]byte(vcapJSON), &vcap); err != nil {
		return fmt.Errorf("failed to parse VCAP_SERVICES: %w", err)
	}

	if len(vcap.SeaweedFS) == 0 {
		return fmt.Errorf("no seaweedfs service found in VCAP_SERVICES")
	}

	creds := vcap.SeaweedFS[0].Credentials
	bucketName = creds.Bucket

	endpoint := creds.Endpoint
	if endpoint == "" {
		endpoint = creds.URI
	}

	region := creds.Region
	if region == "" {
		region = "us-east-1"
	}

	sess, err := session.NewSession(&aws.Config{
		Endpoint:         aws.String(endpoint),
		Region:           aws.String(region),
		Credentials:      credentials.NewStaticCredentials(creds.AccessKey, creds.SecretKey, ""),
		DisableSSL:       aws.Bool(!creds.UseSSL),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("failed to create AWS session: %w", err)
	}

	s3Client = s3.New(sess)
	log.Printf("S3 client initialized: endpoint=%s bucket=%s", endpoint, bucketName)
	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok"}`)
}

func verifyCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	vcapJSON := os.Getenv("VCAP_SERVICES")
	if vcapJSON == "" {
		http.Error(w, "VCAP_SERVICES not set", http.StatusInternalServerError)
		return
	}
	var vcap vcapServices
	if err := json.Unmarshal([]byte(vcapJSON), &vcap); err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse: %v", err), http.StatusInternalServerError)
		return
	}
	if len(vcap.SeaweedFS) == 0 {
		http.Error(w, "No seaweedfs binding found", http.StatusInternalServerError)
		return
	}
	creds := vcap.SeaweedFS[0].Credentials
	result := map[string]interface{}{
		"has_access_key": creds.AccessKey != "",
		"has_secret_key": creds.SecretKey != "",
		"has_bucket":     creds.Bucket != "",
		"has_endpoint":   creds.Endpoint != "" || creds.URI != "",
		"bucket":         creds.Bucket,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func putHandler(w http.ResponseWriter, r *http.Request) {
	_, err := s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(testObjectKey),
		Body:   strings.NewReader(testContent),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("PUT failed: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, `{"status":"ok","key":"%s"}`, testObjectKey)
}

func getHandler(w http.ResponseWriter, r *http.Request) {
	result, err := s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(testObjectKey),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("GET failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Read failed: %v", err), http.StatusInternalServerError)
		return
	}

	// SHA-256 content verification
	hash := sha256.Sum256(body)
	gotHash := hex.EncodeToString(hash[:])
	expectedHash := sha256.Sum256([]byte(testContent))
	expectedHashStr := hex.EncodeToString(expectedHash[:])

	match := gotHash == expectedHashStr
	resp := map[string]interface{}{
		"status":        "ok",
		"content_match": match,
		"sha256":        gotHash,
	}
	if !match {
		resp["status"] = "mismatch"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func multipartHandler(w http.ResponseWriter, r *http.Request) {
	// Create a >5MB payload to test multipart upload
	bigContent := strings.Repeat("SeaweedFS multipart test data. ", 200000) // ~6MB
	_, err := s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("smoke-test-multipart"),
		Body:   strings.NewReader(bigContent),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Multipart PUT failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Cleanup
	s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("smoke-test-multipart"),
	})

	fmt.Fprintf(w, `{"status":"ok","size_bytes":%d}`, len(bigContent))
}

func listHandler(w http.ResponseWriter, r *http.Request) {
	result, err := s3Client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("LIST failed: %v", err), http.StatusInternalServerError)
		return
	}

	keys := make([]string, 0, len(result.Contents))
	for _, obj := range result.Contents {
		keys = append(keys, *obj.Key)
	}
	resp := map[string]interface{}{
		"status": "ok",
		"count":  len(keys),
		"keys":   keys,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	_, err := s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(testObjectKey),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("DELETE failed: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, `{"status":"ok","deleted":"%s"}`, testObjectKey)
}

func verifyTLSHandler(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status":      "ok",
		"tls":         r.TLS != nil,
		"proto":       r.Proto,
		"remote_addr": r.RemoteAddr,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
