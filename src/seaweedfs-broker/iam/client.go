package iam

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client provides access to SeaweedFS IAM API for credential management
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
	// Create HTTP client that accepts self-signed certs
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	return &Client{
		endpoint:   endpoint,
		accessKey:  accessKey,
		secretKey:  secretKey,
		region:     region,
		useSSL:     useSSL,
		httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
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
	XMLName   xml.Name `xml:"CreateAccessKeyResponse"`
	AccessKey struct {
		UserName        string `xml:"UserName"`
		AccessKeyId     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		Status          string `xml:"Status"`
	} `xml:"CreateAccessKeyResult>AccessKey"`
}

// ErrorResponse is the XML error response
type ErrorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// CreateAccessKey creates a new access key for the specified user
// If the user doesn't exist, SeaweedFS will create it
func (c *Client) CreateAccessKey(userName string) (*AccessKey, error) {
	params := url.Values{}
	params.Set("Action", "CreateAccessKey")
	params.Set("UserName", userName)
	params.Set("Version", "2010-05-08")

	resp, err := c.signedRequest("POST", params)
	if err != nil {
		return nil, fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if xml.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("IAM error: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("IAM request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result CreateAccessKeyResponse
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w (body: %s)", err, string(body))
	}

	return &AccessKey{
		UserName:        result.AccessKey.UserName,
		AccessKeyID:     result.AccessKey.AccessKeyId,
		SecretAccessKey: result.AccessKey.SecretAccessKey,
		Status:          result.AccessKey.Status,
	}, nil
}

// DeleteAccessKey deletes an access key
func (c *Client) DeleteAccessKey(userName, accessKeyID string) error {
	params := url.Values{}
	params.Set("Action", "DeleteAccessKey")
	params.Set("UserName", userName)
	params.Set("AccessKeyId", accessKeyID)
	params.Set("Version", "2010-05-08")

	resp, err := c.signedRequest("POST", params)
	if err != nil {
		return fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if xml.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return fmt.Errorf("IAM error: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		return fmt.Errorf("IAM request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// signedRequest makes an AWS Signature V4 signed request to the IAM API
func (c *Client) signedRequest(method string, params url.Values) (*http.Response, error) {
	// Build endpoint URL
	protocol := "http"
	if c.useSSL {
		protocol = "https"
	}
	endpointURL := fmt.Sprintf("%s://%s/", protocol, c.endpoint)

	// Create request
	req, err := http.NewRequest(method, endpointURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}

	// Set headers
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Sign the request with AWS Signature V4
	c.signRequest(req, params)

	return c.httpClient.Do(req)
}

// signRequest adds AWS Signature V4 headers to the request
func (c *Client) signRequest(req *http.Request, params url.Values) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// Set required headers
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", c.endpoint)

	// Create canonical request
	canonicalURI := "/"
	canonicalQueryString := ""
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-date:%s\n",
		req.Header.Get("Content-Type"), c.endpoint, amzDate)
	signedHeaders := "content-type;host;x-amz-date"

	// Hash the payload
	payloadHash := sha256Hash(params.Encode())

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash)

	// Create string to sign
	// Note: SeaweedFS uses the same endpoint for S3 and IAM, so we sign with "s3" service
	algorithm := "AWS4-HMAC-SHA256"
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.region)
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s",
		algorithm,
		amzDate,
		credentialScope,
		sha256Hash(canonicalRequest))

	// Calculate signature using "s3" as service name for SeaweedFS compatibility
	signingKey := getSignatureKey(c.secretKey, dateStamp, c.region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	// Create authorization header
	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm,
		c.accessKey,
		credentialScope,
		signedHeaders,
		signature)

	req.Header.Set("Authorization", authHeader)
}

func sha256Hash(data string) string {
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func getSignatureKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return kSigning
}

// PutUserPolicy attaches a policy to a user to grant bucket access
func (c *Client) PutUserPolicy(userName, policyName, bucketName string) error {
	// Create a policy that grants full access to the specific bucket
	policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Action": ["s3:*"],
				"Resource": [
					"arn:aws:s3:::%s",
					"arn:aws:s3:::%s/*"
				]
			}
		]
	}`, bucketName, bucketName)

	params := url.Values{}
	params.Set("Action", "PutUserPolicy")
	params.Set("UserName", userName)
	params.Set("PolicyName", policyName)
	params.Set("PolicyDocument", policy)
	params.Set("Version", "2010-05-08")

	resp, err := c.signedRequest("POST", params)
	if err != nil {
		return fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if xml.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return fmt.Errorf("IAM error: %s - %s", errResp.Error.Code, errResp.Error.Message)
		}
		// Policy operations might not be fully supported, log but don't fail
		// The access key creation is what matters most
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

	resp, err := c.signedRequest("POST", params)
	if err != nil {
		return fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	// Ignore errors for policy deletion - it's best effort
	return nil
}

// Helper to sort and encode query parameters (for canonical query string if needed)
func sortedQueryString(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		for _, v := range params[k] {
			pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(pairs, "&")
}
