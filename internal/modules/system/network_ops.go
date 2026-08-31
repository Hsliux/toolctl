package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type DNSResult struct {
	Name      string  `json:"name"`
	Address   string  `json:"address"`
	LatencyMS float64 `json:"latencyMs"`
}
type RouteResult struct {
	Target    string `json:"target"`
	Address   string `json:"address"`
	Source    string `json:"source"`
	Interface string `json:"interface,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
}
type ConnectResult struct {
	Target    string  `json:"target"`
	Status    string  `json:"status"`
	Local     string  `json:"local,omitempty"`
	Remote    string  `json:"remote,omitempty"`
	LatencyMS float64 `json:"latencyMs"`
}

func (r *Runner) runNetDNS(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	name := strings.TrimSpace(op.Name)
	started := time.Now()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	elapsed := time.Since(started)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "DNS lookup failed", err), nil
	}
	if len(addresses) == 0 {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "DNS lookup returned no addresses", nil), nil
	}
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, address := range addresses {
		value := DNSResult{name, address.IP.String(), float64(elapsed.Microseconds()) / 1000}
		item, e := resultbuilder.NewItem("DNSResult", address.IP.String(), "", value)
		if e != nil {
			return core.RunOutput{}, e
		}
		output.Items = append(output.Items, item)
	}
	return output, nil
}

func (r *Runner) runNetConnect(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	target := strings.TrimSpace(op.Name)
	if _, _, err := net.SplitHostPort(target); err != nil {
		return failedOutput(v1alpha1.ErrorInvalidArgument, "target must be host:port", err), nil
	}
	timeoutMS := int64(3000)
	if raw, ok := op.Options["connect-timeout"]; ok {
		if err := json.Unmarshal(raw, &timeoutMS); err != nil {
			return failedOutput(v1alpha1.ErrorInvalidArgument, "invalid connect timeout", err), nil
		}
	}
	if timeoutMS < 100 || timeoutMS > 60000 {
		return failedOutput(v1alpha1.ErrorInvalidArgument, "connect timeout must be between 100ms and 1m", fmt.Errorf("invalid timeout")), nil
	}
	dialer := net.Dialer{Timeout: time.Duration(timeoutMS) * time.Millisecond}
	if raw, ok := op.Options["bind"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return failedOutput(v1alpha1.ErrorInvalidArgument, "invalid bind address", fmt.Errorf("invalid string")), nil
		}
		if text != "" {
			ip := net.ParseIP(text)
			if ip == nil {
				return failedOutput(v1alpha1.ErrorInvalidArgument, "bind must be an IP address", fmt.Errorf("invalid IP")), nil
			}
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	started := time.Now()
	connection, err := dialer.DialContext(ctx, "tcp", target)
	elapsed := time.Since(started)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "TCP connection failed", err), nil
	}
	defer connection.Close()
	value := ConnectResult{target, "connected", connection.LocalAddr().String(), connection.RemoteAddr().String(), float64(elapsed.Microseconds()) / 1000}
	item, e := resultbuilder.NewItem("ConnectResult", target, "", value)
	if e != nil {
		return core.RunOutput{}, e
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) runNetRoute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	target := strings.TrimSpace(op.Name)
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, target)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "route target could not be resolved", err), nil
	}
	if len(addresses) == 0 {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "route target resolved to no addresses", nil), nil
	}
	address := addresses[0].IP
	connection, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(address.String(), "9"))
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "route source could not be selected", err), nil
	}
	local := connection.LocalAddr().(*net.UDPAddr).IP
	_ = connection.Close()
	interfaceName := interfaceForIP(local)
	gateway := ""
	if address.To4() != nil {
		gateway = ipv4Gateway(r.deps.Files, address, interfaceName)
	}
	value := RouteResult{target, address.String(), local.String(), interfaceName, gateway}
	item, e := resultbuilder.NewItem("RouteResult", target, "", value)
	if e != nil {
		return core.RunOutput{}, e
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func interfaceForIP(ip net.IP) string {
	interfaces, _ := net.Interfaces()
	for _, item := range interfaces {
		addresses, _ := item.Addrs()
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if ok && network.IP.Equal(ip) {
				return item.Name
			}
		}
	}
	return ""
}
func ipv4Gateway(files core.FileSystem, target net.IP, interfaceName string) string {
	data, err := files.ReadFile("/proc/net/route", 1<<20)
	if err != nil {
		return ""
	}
	targetValue := binaryIPv4(target)
	bestMask, bestGateway := uint32(0), uint32(0)
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 8 || interfaceName != "" && fields[0] != interfaceName {
			continue
		}
		destination, ok1 := parseRouteHex(fields[1])
		gateway, ok2 := parseRouteHex(fields[2])
		mask, ok3 := parseRouteHex(fields[7])
		if !ok1 || !ok2 || !ok3 || targetValue&mask != destination&mask {
			continue
		}
		if maskBits(mask) >= maskBits(bestMask) {
			bestMask, bestGateway = mask, gateway
		}
	}
	if bestGateway == 0 {
		return ""
	}
	return net.IPv4(byte(bestGateway), byte(bestGateway>>8), byte(bestGateway>>16), byte(bestGateway>>24)).String()
}
func parseRouteHex(value string) (uint32, bool) {
	parsed, err := strconv.ParseUint(value, 16, 32)
	return uint32(parsed), err == nil
}
func binaryIPv4(ip net.IP) uint32 {
	value := ip.To4()
	if value == nil {
		return 0
	}
	return uint32(value[0]) | uint32(value[1])<<8 | uint32(value[2])<<16 | uint32(value[3])<<24
}
func maskBits(value uint32) int {
	count := 0
	for value > 0 {
		count += int(value & 1)
		value >>= 1
	}
	return count
}
