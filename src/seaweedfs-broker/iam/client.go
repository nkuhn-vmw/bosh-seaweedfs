package iam

import (
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7/pkg/signer"
)

// Client provides access to SeaweedFS IAM API for credential management.
// Uses the same minio-go SignV4 signing that works for S3 operations.
type Client struct {
	endpoint   string
	accessKey  string
	secretKey  string
	region     string
	useSSL     bool
	httpClient *http.Client
}

// NewClient creates a new IAM client
func NewClient(endpoint, accessKey, secretKey, region string, useSSL bool) *Client {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	log.Printf("IAM Client initialized: endpoint=%s, useSSL=%v, region=%s, accessKey=%s",
		endpoint, useSSL, region, accessKey)

	return &Client{
		endpoint:   endpoint,
		accessKey:  accessKey,
		secretKey:  secretKey,
		region:     region,
		useSSL:     useSSL,
		httpClient: httpClient,
	}
}

// AccessKey represents an IAM access key
type AccessKey struct {
	UserName        string
	AccessKeyID     string
	SecretAccessKey string
	Status          string
}

// CreateAccessKeyResponse is the XML response from CreateAccessKey
type CreateAccessKeyResponse struct {
	XMLName xml.Name `xml:"CreateAccessKeyResponse"`
	Result  struct {
		AccessKey struct {
			UserName        string `xml:"UserName"`
			AccessKeyId     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			Status          string `xml:"Status"`
		} `xml:"AccessKey"`
	} `xml:"CreateAccessKeyResult"`
}

// ErrorResponse is the XML error response
type ErrorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	RequestId string `xml:"RequestId"`
}

// CreateUser creates an IAM user (must be called before CreateAccessKey)
func (c *Client) CreateUser(userName string) error {
	params := url.Values{}
	params.Set("Action", "CreateUser")
	params.Set("UserName", userName)
	params.Set("Version", "2010-05-08")

	log.Printf("IAM: Creating user %s at endpoint %s", userName, c.endpoint)

	_, err := c.doRequest(params)
	if err != nil {
		return fmt.Errorf("CreateUser failed: %w", err)
	}

	log.Printf("IAM: Created user %s", userName)
	return nil
}

// DeleteUser deletes an IAM user
func (c *Client) DeleteUser(userName string) error {
	params := url.Values{}
	params.Set("Action", "DeleteUser")
	params.Set("UserName", userName)
	params.Set("Version", "2010-05-08")

	log.Printf("IAM: Deleting user %s", userName)

	_, err := c.doRequest(params)
	if err != nil {
		return fmt.Errorf("DeleteUser failed: %w", err)
	}

	return nil
}

// CreateAccessKey creates a new access key for the specified user.
// The user must already exist (call CreateUser first).
func (c *Client) CreateAccessKey(userName string) (*AccessKey, error) {
	params := url.Values{}
	params.Set("Action", "CreateAccessKey")
	params.Set("UserName", userName)
	params.Set("Version", "2010-05-08")

	log.Printf("IAM: Creating access key for user %s at endpoint %s", userName, c.endpoint)

	body, err := c.doRequest(params)
	if err != nil {
		return nil, fmt.Errorf("CreateAccessKey failed: %w", err)
	}

	var resp CreateAccessKeyResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse CreateAccessKey response: %w (body: %s)", err, string(body))
	}

	log.Printf("IAM: Created access key %s for user %s", resp.Result.AccessKey.AccessKeyId, userName)

	return &AccessKey{
		UserName:        resp.Result.AccessKey.UserName,
		AccessKeyID:     resp.Result.AccessKey.AccessKeyId,
		SecretAccessKey: resp.Result.AccessKey.SecretAccessKey,
		Status:          resp.Result.AccessKey.Status,
	}, nil
}

// DeleteAccessKey deletes an access key
func (c *Client) DeleteAccessKey(userName, accessKeyID string) error {
	params := url.Values{}
	params.Set("Action", "DeleteAccessKey")
	params.Set("UserName", userName)
	params.Set("AccessKeyId", accessKeyID)
	params.Set("Version", "2010-05-08")

	log.Printf("IAM: Deleting access key %s for user %s", accessKeyID, userName)

	_, err := c.doRequest(params)
	if err != nil {
		return fmt.Errorf("DeleteAccessKey failed: %w", err)
	}

	return nil
}

// PutUserPolicy attaches a policy to a user to grant bucket access
func (c *Client) PutUserPolicy(userName, policyName, bucketName string) error {
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::%s","arn:aws:s3:::%s/*"]}]}`, bucketName, bucketName)

	params := url.Values{}
	params.Set("Action", "PutUserPolicy")
	params.Set("UserName", userName)
	params.Set("PolicyName", policyName)
	params.Set("PolicyDocument", policy)
	params.Set("Version", "2010-05-08")

	log.Printf("IAM: Attaching policy %s to user %s for bucket %s", policyName, userName, bucketName)

	_, err := c.doRequest(params)
	if err != nil {
		log.Printf("Warning: PutUserPolicy failed (may not be supported): %v", err)
		return nil
	}

	return nil
}

// DeleteUserPolicy removes a policy from a user
func (c *Client) DeleteUserPolicy(userName, policyName string) error {
	params := url.Values{}
	params.Set("Action", "DeleteUserPolicy")
	params.Set("UserName", userName)
	params.Set("PolicyName", policyName)
	params.Set("Version", "2010-05-08")

	_, err := c.doRequest(params)
	if err != nil {
		log.Printf("Warning: DeleteUserPolicy failed: %v", err)
	}

	return nil
}

// doRequest makes a signed request to the IAM API using minio-go's SignV4
func (c *Client) doRequest(params url.Values) ([]byte, error) {
	protocol := "http"
	if c.useSSL {
		protocol = "https"
	}

	endpointURL := fmt.Sprintf("%s://%s/", protocol, c.endpoint)

	bodyStr := params.Encode()

	req, err := http.NewRequest("POST", endpointURL, strings.NewReader(bodyStr))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// UNSIGNED-PAYLOAD tells SeaweedFS not to verify the body hash,
	// which avoids mismatches from body recomputation on the server side
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")

	// Use minio-go's proven SignV4 implementation (same code that signs
	// our working S3 bucket operations)
	signedReq := signer.SignV4(*req, c.accessKey, c.secretKey, "", c.region)

	log.Printf("IAM Request: %s %s (Action=%s, body_len=%d, auth=%s)",
		signedReq.Method, endpointURL, params.Get("Action"), len(bodyStr),
		signedReq.Header.Get("Authorization")[:80]+"...")

	resp, err := c.httpClient.Do(signedReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("IAM Response: status=%d, body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if xmlErr := xml.Unmarshal(respBody, &errResp); xmlErr == nil && errResp.Error.Code != "" {
			return nil, fmt.Errorf("IAM error %s: %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("IAM request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
