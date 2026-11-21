// Package stun implements the tiny part of RFC 5389 that Voice2Us needs:
// a UDP Binding request to learn which public ip:port the NAT gave us.
// We run the query through the very socket that will later carry the call,
// so the discovered mapping is the one the peer should blast packets to.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

const magicCookie uint32 = 0x2112A442

// DefaultServers is the list of public STUN servers we try, Russian ones
// included so discovery works even if some foreign hosts are unreachable.
var DefaultServers = []string{
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"stun.miet.ru:3478",
	"stun.sipnet.ru:3478",
}

// Query sends a Binding request to server through conn and returns the
// reflexive (public) address of conn.
func Query(conn *net.UDPConn, server string, timeout time.Duration) (*net.UDPAddr, error) {
	raddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return nil, err
	}

	var tid [12]byte
	if _, err := rand.Read(tid[:]); err != nil {
		return nil, err
	}

	// Binding request: type 0x0001, message length 0, magic cookie, transaction id.
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:2], 0x0001)
	binary.BigEndian.PutUint16(msg[2:4], 0)
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], tid[:])

	if _, err := conn.WriteToUDP(msg, raddr); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1500)
	for {
		conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if addr, ok := parseBindingResponse(buf[:n], tid[:]); ok {
			conn.SetReadDeadline(time.Time{})
			return addr, nil
		}
	}
	conn.SetReadDeadline(time.Time{})
	return nil, errors.New("stun: нет ответа от " + server)
}

func parseBindingResponse(msg, tid []byte) (*net.UDPAddr, bool) {
	if len(msg) < 20 || binary.BigEndian.Uint16(msg[0:2]) != 0x0101 {
		return nil, false
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie || !equalBytes(msg[8:20], tid) {
		return nil, false
	}

	size := int(binary.BigEndian.Uint16(msg[2:4]))
	pos := 20
	for pos+4 <= 20+size && pos+4 <= len(msg) {
		attrType := binary.BigEndian.Uint16(msg[pos : pos+2])
		attrLen := int(binary.BigEndian.Uint16(msg[pos+2 : pos+4]))
		if pos+4+attrLen > len(msg) {
			return nil, false
		}
		val := msg[pos+4 : pos+4+attrLen]

		switch attrType {
		case 0x0020: // XOR-MAPPED-ADDRESS, IPv4
			if attrLen >= 8 && val[1] == 0x01 {
				port := binary.BigEndian.Uint16(val[2:4]) ^ uint16(magicCookie>>16)
				ip := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					ip[i] = val[4+i] ^ msg[4+i]
				}
				return &net.UDPAddr{IP: ip, Port: int(port)}, true
			}
		case 0x0001: // MAPPED-ADDRESS (fallback), IPv4
			if attrLen >= 8 && val[1] == 0x01 {
				port := binary.BigEndian.Uint16(val[2:4])
				ip := net.IP(append([]byte(nil), val[4:8]...))
				return &net.UDPAddr{IP: ip, Port: int(port)}, true
			}
		}
		pos += 4 + ((attrLen + 3) &^ 3)
	}
	return nil, false
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
