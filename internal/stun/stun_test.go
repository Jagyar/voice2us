package stun

import (
	"encoding/binary"
	"testing"
)

func response(tid []byte, attrType uint16, val []byte) []byte {
	m := make([]byte, 20, 40)
	binary.BigEndian.PutUint16(m[0:], 0x0101)
	binary.BigEndian.PutUint16(m[2:], uint16(4+len(val)))
	binary.BigEndian.PutUint32(m[4:], magicCookie)
	copy(m[8:], tid)
	m = binary.BigEndian.AppendUint16(m, attrType)
	m = binary.BigEndian.AppendUint16(m, uint16(len(val)))
	return append(m, val...)
}

func TestParseXorMapped(t *testing.T) {
	tid := []byte("0123456789ab")
	port := uint16(40000)
	val := []byte{0, 1, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(val[2:], port^uint16(magicCookie>>16))
	ip := []byte{203, 0, 113, 7}
	cookie := make([]byte, 4)
	binary.BigEndian.PutUint32(cookie, magicCookie)
	for i := range ip {
		val[4+i] = ip[i] ^ cookie[i]
	}
	addr, ok := parseBindingResponse(response(tid, 0x0020, val), tid)
	if !ok || addr.String() != "203.0.113.7:40000" {
		t.Fatalf("got %v %v", addr, ok)
	}
}

func TestParseRejects(t *testing.T) {
	tid := []byte("0123456789ab")
	val := []byte{0, 1, 0, 80, 1, 2, 3, 4}
	if _, ok := parseBindingResponse(response([]byte("xxxxxxxxxxxx"), 0x0001, val), tid); ok {
		t.Error("чужой transaction id")
	}
	if _, ok := parseBindingResponse([]byte{1, 2, 3}, tid); ok {
		t.Error("короткое сообщение")
	}
	bad := response(tid, 0x0020, val)
	binary.BigEndian.PutUint16(bad[22:], 200) // длина атрибута больше сообщения
	if _, ok := parseBindingResponse(bad, tid); ok {
		t.Error("битая длина атрибута")
	}
}
