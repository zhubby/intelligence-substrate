// Package runtimehost manages approved Linux application images on a trusted
// Docker node. Application traffic is served by a separate, read-only gateway.
package runtimehost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/intelligence-substrate/platform/internal/publisher"
)

type Config struct {
	InstanceID      string  `json:"instanceId"`
	StateDir        string  `json:"stateDir"`
	ProjectID       string  `json:"projectId"`
	GoalID          string  `json:"goalId"`
	AppID           string  `json:"appId"`
	PublicURL       string  `json:"publicUrl"`
	DockerBinary    string  `json:"dockerBinary"`
	DockerHost      string  `json:"dockerHost"`
	ContainerPort   int     `json:"containerPort"`
	HealthPath      string  `json:"healthPath"`
	PersistentData  bool    `json:"persistentData"`
	DataContract    string  `json:"dataContract"`
	EnvironmentFile string  `json:"environmentFile,omitempty"`
	MemoryMB        int     `json:"memoryMB"`
	CPUs            float64 `json:"cpus"`
	PlatformURL     string  `json:"platformUrl,omitempty"`
	WorkerTokenFile string  `json:"workerTokenFile,omitempty"`
}
type Request struct {
	publisher.Request
	ExpectedActive string `json:"expectedActive,omitempty"`
	RollbackTo     string `json:"rollbackTo,omitempty"`
}
type Result struct {
	ExternalID    string         `json:"externalId"`
	State         string         `json:"state"`
	URL           string         `json:"url,omitempty"`
	UpstreamState string         `json:"upstreamState,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Runtime       map[string]any `json:"runtime,omitempty"`
}

var hashRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitRE = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
var idRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)

func (c Config) validate() error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.DockerBinary) || !idRE.MatchString(c.InstanceID) || !idRE.MatchString(c.ProjectID) || !idRE.MatchString(c.GoalID) || !idRE.MatchString(c.AppID) {
		return fmt.Errorf("absolute state/docker paths and fixed project, goal and app required")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("dedicated HTTPS application origin required")
	}
	if !strings.HasPrefix(c.DockerHost, "unix:///") || strings.ContainsAny(c.DockerHost, "\r\n") {
		return fmt.Errorf("explicit local Docker socket required")
	}
	if c.ContainerPort < 1024 || c.ContainerPort > 65535 || !strings.HasPrefix(c.HealthPath, "/") || strings.HasPrefix(c.HealthPath, "//") || strings.ContainsAny(c.HealthPath, "?#\r\n") || len(c.HealthPath) > 500 {
		return fmt.Errorf("unprivileged container port and fixed health path required")
	}
	if c.MemoryMB < 64 || c.MemoryMB > 8192 || c.CPUs <= 0 || c.CPUs > 8 {
		return fmt.Errorf("explicit memory (64..8192 MiB) and CPU (0..8) limits required")
	}
	if !idRE.MatchString(c.DataContract) {
		return fmt.Errorf("validated application data contract required")
	}
	if c.EnvironmentFile != "" && !filepath.IsAbs(c.EnvironmentFile) {
		return fmt.Errorf("environment file must be administrator-owned absolute path")
	}
	return nil
}
func (c Config) owner() string {
	sum := sha256.Sum256([]byte(c.InstanceID + ":" + c.ProjectID + ":" + c.GoalID + ":" + c.AppID))
	return hex.EncodeToString(sum[:])[:24]
}
func (c Config) external(key string) string { return "runtime:" + c.AppID + ":" + key }
func (c Config) externalKey(id string) (string, error) {
	key := strings.TrimPrefix(id, "runtime:"+c.AppID+":")
	if !hashRE.MatchString(key) || key == id {
		return "", fmt.Errorf("external runtime identity does not match configured application")
	}
	return key, nil
}
