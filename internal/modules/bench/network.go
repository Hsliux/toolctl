package bench

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const (
	networkMagic      = "TOOLCT2\n"
	networkHeaderSize = 56
	maxNetworkStreams = 128
)

type networkHandshake struct {
	Session    [16]byte
	Stream     uint32
	Streams    uint32
	Direction  string
	DurationMS uint64
	Rate       uint64
}

type networkTransfer struct {
	sent, received uint64
	duration       time.Duration
}

func (r *Runner) runNetworkClient(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	return r.runNetworkClientWithProgress(ctx, op, nil)
}

func (r *Runner) runNetworkClientWithProgress(ctx context.Context, op v1alpha1.Operation, progress func(NetworkBenchmarkResult) error) (core.RunOutput, error) {
	duration, err := durationOptionValue(op, "duration", 10*time.Second)
	if err != nil || duration < time.Second || duration > time.Hour {
		return failure(v1alpha1.ErrorInvalidArgument, "network duration must be between 1s and 1h", err), nil
	}
	port, err := integerOptionValue(op, "port", 9234)
	if err != nil || port < 1 || port > 65535 {
		return failure(v1alpha1.ErrorInvalidArgument, "network port must be between 1 and 65535", err), nil
	}
	streams, err := integerOptionValue(op, "parallel", 1)
	if err != nil || streams < 1 || streams > maxNetworkStreams {
		return failure(v1alpha1.ErrorInvalidArgument, fmt.Sprintf("network parallel streams must be between 1 and %d", maxNetworkStreams), err), nil
	}
	direction, err := benchmarkStringOption(op, "direction", "send")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	direction = strings.ToLower(direction)
	if direction != "send" && direction != "receive" && direction != "both" {
		return failure(v1alpha1.ErrorInvalidArgument, "network direction must be send, receive, or both", nil), nil
	}
	bindAddress, err := benchmarkStringOption(op, "bind", "")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	rateText, err := benchmarkStringOption(op, "rate", "")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	rate, err := parseNetworkRate(rateText)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	progressInterval := time.Duration(0)
	if progress != nil {
		progressInterval, err = durationOptionValue(op, "interval", time.Second)
		if err != nil || progressInterval < 100*time.Millisecond || progressInterval > time.Minute {
			return failure(v1alpha1.ErrorInvalidArgument, "network live interval must be between 100ms and 1m", err), nil
		}
	}
	peer, err := networkEndpoint(op.Name, int(port))
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	dialer := &net.Dialer{}
	if bindAddress != "" {
		ip := net.ParseIP(bindAddress)
		if ip == nil {
			return failure(v1alpha1.ErrorInvalidArgument, "network bind address must be an IPv4 or IPv6 address", nil), nil
		}
		dialer.LocalAddr = &net.TCPAddr{IP: ip}
	}
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return core.RunOutput{}, fmt.Errorf("generate network session ID: %w", err)
	}
	connections := make([]*net.TCPConn, int(streams))
	connectStarted := time.Now()
	for index := range connections {
		connection, err := dialer.DialContext(ctx, "tcp", peer)
		if err != nil {
			closeTCPConnections(connections)
			return failure(v1alpha1.ErrorExecutionFailed, "network benchmark peer could not be reached", err), nil
		}
		tcp, ok := connection.(*net.TCPConn)
		if !ok {
			_ = connection.Close()
			closeTCPConnections(connections)
			return failure(v1alpha1.ErrorExecutionFailed, "network benchmark did not establish a TCP connection", nil), nil
		}
		connections[index] = tcp
		header := networkHandshake{Session: session, Stream: uint32(index), Streams: uint32(streams), Direction: direction, DurationMS: uint64(duration.Milliseconds()), Rate: rate}
		if err := writeNetworkHandshake(tcp, header); err != nil {
			closeTCPConnections(connections)
			return failure(v1alpha1.ErrorExecutionFailed, "network benchmark handshake failed", err), nil
		}
	}
	connectLatency := time.Since(connectStarted)
	defer closeTCPConnections(connections)
	transfer, err := runNetworkTransfer(ctx, connections, direction, duration, rate, progressInterval, func(snapshot networkTransfer) error {
		if progress == nil {
			return nil
		}
		result := newNetworkResult("client", peer, direction, int(streams), snapshot)
		result.ConnectLatencyMS = float64(connectLatency.Microseconds()) / 1000
		return progress(result)
	})
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "network benchmark transfer failed", err), nil
	}
	result := newNetworkResult("client", peer, direction, int(streams), transfer)
	result.ConnectLatencyMS = float64(connectLatency.Microseconds()) / 1000
	return networkResult(result)
}

func (r *Runner) runNetworkServer(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	listen, err := benchmarkStringOption(op, "listen", ":9234")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "network benchmark listener could not start", err), nil
	}
	return r.serveNetworkListener(ctx, listener)
}

func (r *Runner) serveNetworkListener(ctx context.Context, listener net.Listener) (core.RunOutput, error) {
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	defer close(done)
	first, header, err := acceptNetworkStream(ctx, listener)
	if err != nil {
		return networkAcceptFailure(ctx, err)
	}
	connections := make([]*net.TCPConn, int(header.Streams))
	connections[header.Stream] = first
	defer closeTCPConnections(connections)
	for accepted := 1; accepted < int(header.Streams); accepted++ {
		connection, candidate, err := acceptNetworkStream(ctx, listener)
		if err != nil {
			return networkAcceptFailure(ctx, err)
		}
		if candidate.Session != header.Session || candidate.Streams != header.Streams || candidate.Direction != header.Direction || candidate.DurationMS != header.DurationMS || candidate.Rate != header.Rate || candidate.Stream >= header.Streams || connections[candidate.Stream] != nil {
			_ = connection.Close()
			return failure(v1alpha1.ErrorExecutionFailed, "network benchmark stream handshake does not match the session", nil), nil
		}
		connections[candidate.Stream] = connection
	}
	duration := time.Duration(header.DurationMS) * time.Millisecond
	if header.Direction == "latency" {
		return r.runNetworkEchoServer(ctx, first)
	}
	serverDirection := oppositeNetworkDirection(header.Direction)
	transfer, err := runNetworkTransfer(ctx, connections, serverDirection, duration, header.Rate, 0, nil)
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "network benchmark transfer failed", err), nil
	}
	peer := first.RemoteAddr().String()
	return networkResult(newNetworkResult("server", peer, serverDirection, int(header.Streams), transfer))
}

func acceptNetworkStream(ctx context.Context, listener net.Listener) (*net.TCPConn, networkHandshake, error) {
	connection, err := listener.Accept()
	if err != nil {
		return nil, networkHandshake{}, err
	}
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		return nil, networkHandshake{}, fmt.Errorf("accepted connection is not TCP")
	}
	handshakeDeadline := time.Now().Add(10 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	_ = tcp.SetReadDeadline(handshakeDeadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = tcp.SetReadDeadline(time.Now())
		case <-done:
		}
	}()
	header, err := readNetworkHandshake(tcp)
	close(done)
	_ = tcp.SetReadDeadline(time.Time{})
	if err != nil {
		_ = tcp.Close()
		return nil, networkHandshake{}, err
	}
	if err := validateNetworkHandshake(header); err != nil {
		_ = tcp.Close()
		return nil, networkHandshake{}, err
	}
	return tcp, header, nil
}

func networkAcceptFailure(ctx context.Context, err error) (core.RunOutput, error) {
	if ctx.Err() != nil {
		return core.RunOutput{}, ctx.Err()
	}
	return failure(v1alpha1.ErrorExecutionFailed, "network benchmark connection could not be accepted", err), nil
}

func writeNetworkHandshake(writer io.Writer, header networkHandshake) error {
	buffer := make([]byte, networkHeaderSize)
	copy(buffer[0:8], networkMagic)
	copy(buffer[8:24], header.Session[:])
	binary.BigEndian.PutUint32(buffer[24:28], header.Stream)
	binary.BigEndian.PutUint32(buffer[28:32], header.Streams)
	buffer[32] = networkDirectionCode(header.Direction)
	binary.BigEndian.PutUint64(buffer[33:41], header.DurationMS)
	binary.BigEndian.PutUint64(buffer[41:49], header.Rate)
	return writeAll(writer, buffer)
}

func writeAll(writer io.Writer, buffer []byte) error {
	for len(buffer) > 0 {
		written, err := writer.Write(buffer)
		buffer = buffer[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readNetworkHandshake(reader io.Reader) (networkHandshake, error) {
	buffer := make([]byte, networkHeaderSize)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return networkHandshake{}, err
	}
	if string(buffer[0:8]) != networkMagic {
		return networkHandshake{}, fmt.Errorf("invalid network benchmark magic")
	}
	header := networkHandshake{Stream: binary.BigEndian.Uint32(buffer[24:28]), Streams: binary.BigEndian.Uint32(buffer[28:32]), Direction: networkDirectionName(buffer[32]), DurationMS: binary.BigEndian.Uint64(buffer[33:41]), Rate: binary.BigEndian.Uint64(buffer[41:49])}
	copy(header.Session[:], buffer[8:24])
	return header, nil
}

func validateNetworkHandshake(header networkHandshake) error {
	if header.Streams < 1 || header.Streams > maxNetworkStreams || header.Stream >= header.Streams {
		return fmt.Errorf("invalid network stream count or index")
	}
	if !validNetworkDirection(header.Direction) {
		return fmt.Errorf("invalid network direction")
	}
	if header.DurationMS < 1000 || header.DurationMS > uint64(time.Hour.Milliseconds()) {
		return fmt.Errorf("invalid network duration")
	}
	if header.Direction == "latency" && header.Streams != 1 {
		return fmt.Errorf("latency session requires one stream")
	}
	return nil
}

func runNetworkTransfer(ctx context.Context, connections []*net.TCPConn, direction string, duration time.Duration, rate uint64, progressInterval time.Duration, progress func(networkTransfer) error) (networkTransfer, error) {
	started := time.Now()
	deadline := started.Add(duration)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			for _, connection := range connections {
				if connection != nil {
					_ = connection.SetDeadline(time.Now())
				}
			}
		case <-done:
		}
	}()
	defer close(done)
	var sent, received atomic.Uint64
	errorsChannel := make(chan error, len(connections)*2)
	var group sync.WaitGroup
	perStreamRate := uint64(0)
	if rate > 0 {
		perStreamRate = rate / uint64(len(connections))
		if perStreamRate == 0 {
			perStreamRate = 1
		}
	}
	for _, connection := range connections {
		if direction == "send" || direction == "both" {
			group.Add(1)
			go func(connection *net.TCPConn) {
				defer group.Done()
				errorsChannel <- networkWriteLoop(ctx, connection, deadline, perStreamRate, &sent)
			}(connection)
		}
		if direction == "receive" || direction == "both" {
			group.Add(1)
			go func(connection *net.TCPConn) {
				defer group.Done()
				errorsChannel <- networkReadLoop(ctx, connection, deadline.Add(5*time.Second), &received)
			}(connection)
		}
	}
	groupDone := make(chan struct{})
	go func() {
		group.Wait()
		close(groupDone)
	}()
	if progress != nil && progressInterval > 0 {
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		waiting := true
		for waiting {
			select {
			case <-groupDone:
				waiting = false
			case <-ticker.C:
				if err := progress(networkTransfer{sent: sent.Load(), received: received.Load(), duration: time.Since(started)}); err != nil {
					for _, connection := range connections {
						_ = connection.SetDeadline(time.Now())
					}
					<-groupDone
					return networkTransfer{}, err
				}
			case <-ctx.Done():
				<-groupDone
				return networkTransfer{}, ctx.Err()
			}
		}
	} else {
		<-groupDone
	}
	close(errorsChannel)
	if ctx.Err() != nil {
		return networkTransfer{}, ctx.Err()
	}
	for err := range errorsChannel {
		if err != nil {
			return networkTransfer{}, err
		}
	}
	return networkTransfer{sent: sent.Load(), received: received.Load(), duration: time.Since(started)}, nil
}

func networkWriteLoop(ctx context.Context, connection *net.TCPConn, deadline time.Time, rate uint64, total *atomic.Uint64) error {
	bufferSize := uint64(1 << 20)
	if rate > 0 && rate/10 < bufferSize {
		bufferSize = rate / 10
		if bufferSize == 0 {
			bufferSize = 1
		}
	}
	buffer := make([]byte, int(bufferSize))
	for index := range buffer {
		buffer[index] = byte(index)
	}
	_ = connection.SetWriteDeadline(deadline)
	started := time.Now()
	localBytes := uint64(0)
	for time.Now().Before(deadline) {
		written, err := connection.Write(buffer)
		total.Add(uint64(written))
		localBytes += uint64(written)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() && !time.Now().Before(deadline) {
				break
			}
			return err
		}
		if rate > 0 {
			expected := time.Duration(float64(localBytes) / float64(rate) * float64(time.Second))
			if wait := expected - time.Since(started); wait > 0 {
				if remaining := time.Until(deadline); wait > remaining {
					wait = remaining
				}
				if wait <= 0 {
					break
				}
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	return connection.CloseWrite()
}

func networkReadLoop(ctx context.Context, connection *net.TCPConn, deadline time.Time, total *atomic.Uint64) error {
	_ = connection.SetReadDeadline(deadline)
	buffer := make([]byte, 1<<20)
	for {
		read, err := connection.Read(buffer)
		total.Add(uint64(read))
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func newNetworkResult(role, peer, direction string, streams int, transfer networkTransfer) NetworkBenchmarkResult {
	seconds := transfer.duration.Seconds()
	aggregate := transfer.sent + transfer.received
	return NetworkBenchmarkResult{
		Role: role, Peer: peer, Direction: direction, Streams: streams,
		Bytes: aggregate, BytesPerSecond: uint64(float64(aggregate) / seconds), DurationMS: transfer.duration.Milliseconds(),
		SentBytes: transfer.sent, SentBytesPerSecond: uint64(float64(transfer.sent) / seconds),
		ReceivedBytes: transfer.received, ReceivedBytesPerSecond: uint64(float64(transfer.received) / seconds),
	}
}

func closeTCPConnections(connections []*net.TCPConn) {
	for _, connection := range connections {
		if connection != nil {
			_ = connection.Close()
		}
	}
}

func validNetworkDirection(direction string) bool {
	return direction == "send" || direction == "receive" || direction == "both" || direction == "latency"
}

func oppositeNetworkDirection(direction string) string {
	if direction == "send" {
		return "receive"
	}
	if direction == "receive" {
		return "send"
	}
	return direction
}

func networkDirectionCode(direction string) byte {
	switch direction {
	case "send":
		return 1
	case "receive":
		return 2
	case "both":
		return 3
	case "latency":
		return 4
	default:
		return 0
	}
}

func networkDirectionName(code byte) string {
	switch code {
	case 1:
		return "send"
	case 2:
		return "receive"
	case 3:
		return "both"
	case 4:
		return "latency"
	default:
		return ""
	}
}

func (r *Runner) runNetworkLatency(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	count, err := integerOptionValue(op, "count", 100)
	if err != nil || count < 1 || count > 100000 {
		return failure(v1alpha1.ErrorInvalidArgument, "latency count must be between 1 and 100000", err), nil
	}
	interval, err := durationOptionValue(op, "interval", 10*time.Millisecond)
	if err != nil || interval < time.Millisecond || interval > time.Second {
		return failure(v1alpha1.ErrorInvalidArgument, "latency interval must be between 1ms and 1s", err), nil
	}
	if time.Duration(count)*interval > time.Hour {
		return failure(v1alpha1.ErrorInvalidArgument, "latency test duration must not exceed 1h", nil), nil
	}
	port, err := integerOptionValue(op, "port", 9234)
	if err != nil || port < 1 || port > 65535 {
		return failure(v1alpha1.ErrorInvalidArgument, "network port must be between 1 and 65535", err), nil
	}
	peer, err := networkEndpoint(op.Name, int(port))
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	dialer := net.Dialer{}
	bind, err := benchmarkStringOption(op, "bind", "")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	if bind != "" {
		ip := net.ParseIP(bind)
		if ip == nil {
			return failure(v1alpha1.ErrorInvalidArgument, "network bind address must be an IPv4 or IPv6 address", nil), nil
		}
		dialer.LocalAddr = &net.TCPAddr{IP: ip}
	}
	connection, err := dialer.DialContext(ctx, "tcp", peer)
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "network benchmark peer could not be reached", err), nil
	}
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return core.RunOutput{}, err
	}
	sessionDuration := time.Duration(count)*interval + 5*time.Second
	if sessionDuration < time.Second {
		sessionDuration = time.Second
	}
	if sessionDuration > time.Hour {
		sessionDuration = time.Hour
	}
	header := networkHandshake{Session: session, Streams: 1, Direction: "latency", DurationMS: uint64(sessionDuration.Milliseconds())}
	if err := writeNetworkHandshake(tcp, header); err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "network benchmark handshake failed", err), nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = tcp.SetDeadline(deadline)
	}
	samples := make([]float64, 0, int(count))
	payload := make([]byte, 8)
	for index := int64(0); index < count; index++ {
		binary.BigEndian.PutUint64(payload, uint64(index))
		started := time.Now()
		if err := writeAll(tcp, payload); err != nil {
			return failure(v1alpha1.ErrorExecutionFailed, "latency sample send failed", err), nil
		}
		reply := make([]byte, 8)
		if _, err := io.ReadFull(tcp, reply); err != nil {
			return failure(v1alpha1.ErrorExecutionFailed, "latency sample receive failed", err), nil
		}
		if binary.BigEndian.Uint64(reply) != uint64(index) {
			return failure(v1alpha1.ErrorExecutionFailed, "latency echo sequence mismatch", nil), nil
		}
		samples = append(samples, float64(time.Since(started).Microseconds())/1000)
		if index+1 < count {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return core.RunOutput{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	_ = tcp.CloseWrite()
	sort.Float64s(samples)
	sum := float64(0)
	for _, sample := range samples {
		sum += sample
	}
	result := NetworkLatencyResult{Peer: peer, Count: len(samples), MinMS: samples[0], AverageMS: sum / float64(len(samples)), P50MS: latencyPercentile(samples, 50), P95MS: latencyPercentile(samples, 95), P99MS: latencyPercentile(samples, 99), MaxMS: samples[len(samples)-1]}
	item, e := resultbuilder.NewItem("NetworkLatencyResult", peer, "", result)
	if e != nil {
		return core.RunOutput{}, e
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) runNetworkEchoServer(ctx context.Context, connection *net.TCPConn) (core.RunOutput, error) {
	started := time.Now()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-done:
		}
	}()
	defer close(done)
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	buffer := make([]byte, 8)
	count := uint64(0)
	for {
		_, err := io.ReadFull(connection, buffer)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return core.RunOutput{}, ctx.Err()
			}
			return failure(v1alpha1.ErrorExecutionFailed, "latency echo receive failed", err), nil
		}
		if err := writeAll(connection, buffer); err != nil {
			return failure(v1alpha1.ErrorExecutionFailed, "latency echo send failed", err), nil
		}
		count++
	}
	elapsed := time.Since(started)
	transfer := networkTransfer{sent: count * 8, received: count * 8, duration: elapsed}
	return networkResult(newNetworkResult("server", connection.RemoteAddr().String(), "latency", 1, transfer))
}

func latencyPercentile(samples []float64, percent int) float64 {
	if len(samples) == 0 {
		return 0
	}
	index := (len(samples)*percent+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(samples) {
		index = len(samples) - 1
	}
	return samples[index]
}

func networkEndpoint(value string, defaultPort int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("network peer is empty")
	}
	if host, portText, err := net.SplitHostPort(value); err == nil {
		port, parseErr := strconv.Atoi(portText)
		if strings.TrimSpace(host) == "" || parseErr != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid network peer %q", value)
		}
		return value, nil
	}
	if strings.Contains(value, ":") && net.ParseIP(value) == nil {
		return "", fmt.Errorf("invalid network peer %q", value)
	}
	return net.JoinHostPort(value, strconv.Itoa(defaultPort)), nil
}

func durationOptionValue(op v1alpha1.Operation, name string, fallback time.Duration) (time.Duration, error) {
	milliseconds := fallback.Milliseconds()
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &milliseconds); err != nil {
			return 0, err
		}
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func integerOptionValue(op v1alpha1.Operation, name string, fallback int64) (int64, error) {
	value := fallback
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, err
		}
	}
	return value, nil
}

func parseNetworkRate(value string) (uint64, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "0" || value == "unlimited" {
		return 0, nil
	}
	multipliers := []struct {
		suffix     string
		multiplier float64
	}{
		{"gibps", float64(uint64(1)<<30) / 8}, {"mibps", float64(uint64(1)<<20) / 8}, {"kibps", float64(uint64(1)<<10) / 8},
		{"gbps", 1e9 / 8}, {"mbps", 1e6 / 8}, {"kbps", 1e3 / 8}, {"bps", 1.0 / 8},
	}
	for _, unit := range multipliers {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("invalid network rate %q", value)
		}
		bytesPerSecond := parsed * unit.multiplier
		if bytesPerSecond < 1 || bytesPerSecond > float64(^uint64(0)) {
			return 0, fmt.Errorf("network rate %q is outside the supported range", value)
		}
		return uint64(bytesPerSecond), nil
	}
	return 0, fmt.Errorf("network rate must use bps, Kbps, Mbps, Gbps, Kibps, Mibps, or Gibps")
}

func networkResult(value NetworkBenchmarkResult) (core.RunOutput, error) {
	item, err := resultbuilder.NewItem("NetworkBenchmarkResult", value.Peer, "", value)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}
