package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAgentResponseBytes bounds how much of an AGENT http (or IMDS)
// response this agent will ever buffer in memory (T104). The 5s/10s
// client timeouts below bound the *duration* of a fetch but not its
// *volume* -- over a virtio-net link, a misconfigured or hostile
// endpoint can still deliver hundreds of MB within a few seconds, and in
// a small guest (512 MB default, see cmd/cnimbus/run.go) that's an OOM
// kill. Because T10 sets PID 1's oom_score_adj to -1000, the kernel
// picks the next-best victim instead -- the user's actual workload, not
// this agent -- so the observable symptom is "my app gets killed at
// random intervals" with no visible connection to this polling loop.
// 1 MiB is generous for a KV document or IMDS response.
const maxAgentResponseBytes = 1 << 20

// readLimitedBody reads resp.Body up to maxAgentResponseBytes+1 bytes --
// the "+1" is what turns "the body happened to be exactly the limit" and
// "the body was truncated because it exceeded the limit" into two
// distinguishable read lengths, so an over-length response is reported
// as the error it is rather than silently accepted as a truncated
// success.
func readLimitedBody(url string, resp *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAgentResponseBytes {
		return nil, fmt.Errorf("response from %s exceeds the %d-byte limit this agent will buffer", url, maxAgentResponseBytes)
	}
	return data, nil
}

// httpFetch is the AGENT http kind: a plain GET, with whatever headers
// the Nimbusfile's AGENT header lines declared. Previously done by
// BusyBox's own wget (see internal/rootfs's old buildAgentScript);
// folded in here so it shares this binary's loop/atomic-write plumbing
// with every other kind instead of being its own shell script.
func httpFetch(url string, headers map[string]string) func() ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	return func() ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := readLimitedBody(url, resp)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %s: %s", url, resp.Status, string(data))
		}
		return data, nil
	}
}

func doRequest(method, url string, headers map[string]string, body string) ([]byte, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readLimitedBody(url, resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: HTTP %s: %s", method, url, resp.Status, string(data))
	}
	return data, nil
}

// awsFetch implements EC2 IMDSv2: PUT /latest/api/token with a TTL
// header returns the token as a plain-text body; the actual metadata
// GET then carries that token back as its own header. BusyBox's wget
// has no PUT support at all (only GET/POST), which is why this needs to
// be a real HTTP client rather than a shell script.
func awsFetch(path string) ([]byte, error) {
	token, err := doRequest("PUT", "http://169.254.169.254/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "21600"}, "")
	if err != nil {
		return nil, fmt.Errorf("fetching IMDSv2 token: %w", err)
	}
	return doRequest("GET", "http://169.254.169.254/latest/meta-data/"+path,
		map[string]string{"X-aws-ec2-metadata-token": string(token)}, "")
}

// ibmFetch implements IBM Cloud VPC's instance identity + metadata
// service: PUT /instance_identity/v1/token with a Metadata-Flavor
// header and a small JSON body returns {"access_token": "...", ...};
// the metadata GET then carries that token as a bearer Authorization
// header.
func ibmFetch(path string) ([]byte, error) {
	const version = "2022-03-01"
	tokenResp, err := doRequest("PUT",
		"http://169.254.169.254/instance_identity/v1/token?version="+version,
		map[string]string{"Metadata-Flavor": "ibm", "Content-Type": "application/json"},
		`{"expires_in": 3600}`)
	if err != nil {
		return nil, fmt.Errorf("fetching IBM VPC identity token: %w", err)
	}
	token, err := extractJSONString(tokenResp, "access_token")
	if err != nil {
		return nil, fmt.Errorf("parsing IBM VPC identity token response: %w", err)
	}
	return doRequest("GET", "http://169.254.169.254/metadata/v1/"+path+"?version="+version,
		map[string]string{"Authorization": "Bearer " + token}, "")
}

// extractJSONString pulls one top-level string field out of a JSON
// object without a full struct -- avoids depending on the response
// having no other fields this agent doesn't otherwise care about.
func extractJSONString(data []byte, field string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", err
	}
	v, ok := obj[field]
	if !ok {
		return "", fmt.Errorf("no %q field in response", field)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%q field is not a string", field)
	}
	return s, nil
}
