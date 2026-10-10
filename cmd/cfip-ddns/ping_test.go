package main

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestEchoPacketValidation(t *testing.T) {
	payload := []byte("cfip-health-check")
	request := echoPacket(123, 4, payload)
	if icmpChecksum(request) != 0 || validEcho(request, 123, 4, payload) {
		t.Fatal("invalid request packet")
	}
	reply := append([]byte(nil), request...)
	reply[0] = 0
	reply[2], reply[3] = 0, 0
	binary.BigEndian.PutUint16(reply[2:4], icmpChecksum(reply))
	if !validEcho(reply, 123, 4, payload) {
		t.Fatal("valid echo reply rejected")
	}
	if validEcho(reply, 124, 4, payload) || validEcho(reply, 123, 5, payload) || validEcho(reply, 123, 4, []byte("other")) {
		t.Fatal("unrelated echo reply accepted")
	}
	header := make([]byte, 20)
	header[0] = 0x45
	if !validEcho(append(header, reply...), 123, 4, payload) {
		t.Fatal("raw IPv4 reply rejected")
	}
	reply[len(reply)-1] ^= 1
	if validEcho(reply, 123, 4, payload) {
		t.Fatal("corrupt reply accepted")
	}
	for n := 0; n < 8; n++ {
		if validEcho(reply[:n], 123, 4, payload) {
			t.Fatal("truncated reply accepted")
		}
	}
}

func TestPingLoopback(t *testing.T) {
	conn, _, _, err := openPing(net.ParseIP("127.0.0.1"))
	if err != nil {
		t.Skipf("ICMP permission unavailable: %v", err)
	}
	conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	count, avg, err := pingIP(ctx, "127.0.0.1", 100)
	if err != nil || count != 4 || avg < 0 || avg > 100 {
		t.Fatalf("loopback ping: count=%d avg=%v err=%v", count, avg, err)
	}
}
func TestPingCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _, err := pingIP(ctx, "127.0.0.1", 100)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("ping cancellation failed")
	}
}
