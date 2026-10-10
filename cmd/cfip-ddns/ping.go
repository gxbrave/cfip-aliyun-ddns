package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"syscall"
	"time"
)

func icmpChecksum(b []byte) uint16 {
	var sum uint32
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
func echoPacket(id, seq uint16, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = 8
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[8:], payload)
	binary.BigEndian.PutUint16(b[2:4], icmpChecksum(b))
	return b
}
func validEcho(b []byte, id, seq uint16, payload []byte) bool {
	// Raw IPv4 sockets may include the IP header; ping sockets do not.
	if len(b) > 0 && b[0]>>4 == 4 {
		n := int(b[0]&15) * 4
		if n < 20 || n > len(b) {
			return false
		}
		b = b[n:]
	}
	return len(b) == 8+len(payload) && b[0] == 0 && b[1] == 0 && binary.BigEndian.Uint16(b[4:6]) == id && binary.BigEndian.Uint16(b[6:8]) == seq && bytes.Equal(b[8:], payload) && icmpChecksum(b) == 0
}
func openPing(ip net.IP) (net.PacketConn, net.Addr, uint16, error) {
	// Linux ping sockets work without raw-socket privileges when ping_group_range permits.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if err == nil {
		err = syscall.Bind(fd, &syscall.SockaddrInet4{})
		if err == nil {
			f := os.NewFile(uintptr(fd), "icmp-ping")
			conn, e := net.FilePacketConn(f)
			f.Close()
			if e == nil {
				if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
					return conn, &net.UDPAddr{IP: ip}, uint16(addr.Port), nil
				}
				conn.Close()
			}
		} else {
			syscall.Close(fd)
		}
	}
	// systemd grants only CAP_NET_RAW for machines with restrictive ping_group_range.
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, nil, 0, fmt.Errorf("无法打开 ICMP socket（检查 ping 权限）: %w", err)
	}
	id := make([]byte, 2)
	if _, err = rand.Read(id); err != nil {
		conn.Close()
		return nil, nil, 0, err
	}
	return conn, &net.IPAddr{IP: ip}, binary.BigEndian.Uint16(id), nil
}
func pingIP(ctx context.Context, ip string, maxLatency float64) (int, float64, error) {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return 0, 0, fmt.Errorf("ping 仅支持 IPv4")
	}
	conn, target, id, err := openPing(parsed)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	payload := make([]byte, 16)
	if _, err = rand.Read(payload); err != nil {
		return 0, 0, err
	}
	timeout := time.Duration(min(max(maxLatency*2, 1000), 10000)) * time.Millisecond
	successes := 0
	total := time.Duration(0)
	buf := make([]byte, 1500)
	for seq := uint16(1); seq <= 4; seq++ {
		if ctx.Err() != nil {
			return successes, 0, ctx.Err()
		}
		start := time.Now()
		deadline := start.Add(timeout)
		conn.SetDeadline(deadline)
		if _, err = conn.WriteTo(echoPacket(id, seq, payload), target); err == nil {
			for {
				n, source, e := conn.ReadFrom(buf)
				if e != nil {
					break
				}
				var sender net.IP
				switch addr := source.(type) {
				case *net.IPAddr:
					sender = addr.IP
				case *net.UDPAddr:
					sender = addr.IP
				}
				if sender.Equal(parsed) && validEcho(buf[:n], id, seq, payload) {
					successes++
					total += time.Since(start)
					break
				}
			}
		}
		if seq < 4 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return successes, 0, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if ctx.Err() != nil {
		return successes, 0, ctx.Err()
	}
	avg := float64(0)
	if successes > 0 {
		avg = float64(total) / float64(time.Millisecond) / float64(successes)
	}
	return successes, avg, nil
}
func measure(ctx context.Context, ip string, maxLatency float64) bool {
	successes, avg, err := pingIP(ctx, ip, maxLatency)
	if err != nil {
		log.Printf("域名 IP %s ICMP ping 失败: %v", ip, err)
		return false
	}
	log.Printf("域名 IP %s ICMP ping 平均延迟 %.2f ms，成功 %d/4", ip, avg, successes)
	return successes >= 3 && avg <= maxLatency
}
