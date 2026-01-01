package bpjs

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"gotrol/internal/config"
)

type Client struct {
	creds      *config.BPJSCredentials
	httpClient *http.Client
}

type UpdateWaktuRequest struct {
	KodeBooking string `json:"kodebooking"`
	TaskID      int    `json:"taskid"`
	Waktu       int64  `json:"waktu"`
}

type BPJSResponse struct {
	Metadata struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"metadata"`
	Response interface{} `json:"response,omitempty"`
}

func NewClient(creds *config.BPJSCredentials) *Client {
	return &Client{
		creds: creds,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) generateSignature(timestamp string) string {
	message := c.creds.ConsID + "&" + timestamp
	h := hmac.New(sha256.New, []byte(c.creds.SecretKey))
	h.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (c *Client) getTimestamp() string {
	return strconv.FormatInt(time.Now().UTC().Unix(), 10)
}

func (c *Client) UpdateWaktu(kodeBooking string, taskID int, waktuMs int64) (*BPJSResponse, error) {
	if c.creds.AntrianURL == "" {
		return nil, fmt.Errorf("BPJS Antrian URL not configured")
	}

	url := c.creds.AntrianURL + "antrean/updatewaktu"

	reqBody := UpdateWaktuRequest{
		KodeBooking: kodeBooking,
		TaskID:      taskID,
		Waktu:       waktuMs,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	timestamp := c.getTimestamp()
	signature := c.generateSignature(timestamp)

	req.Header.Set("Content-Type", "Application/x-www-form-urlencoded")
	req.Header.Set("X-cons-id", c.creds.ConsID)
	req.Header.Set("X-timestamp", timestamp)
	req.Header.Set("X-signature", signature)
	req.Header.Set("user_key", c.creds.UserKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var bpjsResp BPJSResponse
	if err := json.Unmarshal(body, &bpjsResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w, body: %s", err, string(body))
	}

	return &bpjsResp, nil
}

func (r *BPJSResponse) IsSuccess() bool {
	return r.Metadata.Code == 200
}
