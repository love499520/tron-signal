package auth

import (
	"net"
	"strings"
)

// InWhitelist
// 判断请求 IP 是否在白名单中
// 白名单支持：
// - 精确 IP（1.2.3.4）
// - CIDR（1.2.3.0/24）
func InWhitelist(ip string, whitelist []string) bool {
	// 处理 X-Forwarded-For 等情况，只取第一个
	if strings.Contains(ip, ",") {
		ip = strings.TrimSpace(strings.Split(ip, ",")[0])
	}

	reqIP := net.ParseIP(ip)
	if reqIP == nil {
		return false
	}

	for _, w := range whitelist {
		// CIDR
		if strings.Contains(w, "/") {
			_, cidr, err := net.ParseCIDR(w)
			if err == nil && cidr.Contains(reqIP) {
				return true
			}
			continue
		}

		// 精确 IP
		if reqIP.Equal(net.ParseIP(w)) {
			return true
		}
	}
	return false
}
