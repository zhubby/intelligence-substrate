package platform

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ValidateAXBackend accepts historical CLI records and an additive, strictly
// public mTLS identity. It never accepts credential material or local paths.
func ValidateAXBackend(backend M) error {
	if len(backend) != 4 && len(backend) != 5 {
		return invalid("需要完整固定的 AX 后端")
	}
	for _, key := range []string{"context", "namespace", "atespace", "image"} {
		if _, ok := backend[key].(string); !ok {
			return invalid("AX 后端字段必须为字符串")
		}
	}
	label := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,61}[a-z0-9]$|^[a-z0-9]$`)
	if !regexp.MustCompile(`^[^\s\x00-\x1f\x7f]{0,512}$`).MatchString(S(backend, "context")) || !label.MatchString(S(backend, "namespace")) || !label.MatchString(S(backend, "atespace")) || !regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`).MatchString(S(backend, "image")) {
		return invalid("AX 后端需要有效空间与固定镜像摘要")
	}
	if len(backend) == 4 {
		return nil
	}
	transport := Obj(backend["transport"])
	if len(transport) != 3 || S(transport, "version") != "mtls-v1" {
		return invalid("AX 连接身份版本无效")
	}
	for _, role := range []string{"control", "guest"} {
		identity := Obj(transport[role])
		if len(identity) != 5 {
			return invalid("AX 连接身份字段不完整")
		}
		for _, key := range []string{"endpoint", "serverName", "serverSPKI", "caDigest", "clientCertDigest"} {
			if _, ok := identity[key].(string); !ok {
				return invalid("AX 连接身份字段必须为字符串")
			}
		}
		host, port, e := net.SplitHostPort(S(identity, "endpoint"))
		number, pe := strconv.Atoi(port)
		if e != nil || pe != nil || number < 1 || number > 65535 || len(S(identity, "endpoint")) > 512 || !(net.ParseIP(host) != nil || axDNSName(host)) || !axDNSName(S(identity, "serverName")) {
			return invalid("AX 连接需要明确的主机端口与 TLS 服务名")
		}
		for _, key := range []string{"serverSPKI", "caDigest", "clientCertDigest"} {
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(S(identity, key)) {
				return invalid("AX 连接身份摘要必须为 SHA256")
			}
		}
	}
	return nil
}

func axDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`).MatchString(part) {
			return false
		}
	}
	return true
}
