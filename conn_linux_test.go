//+build linux

package netlink

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/mdlayher/netlink/nlenc"
)

func TestLinuxConn_bind(t *testing.T) {
	s := &testSocket{}
	if _, err := bind(s, &Config{}); err != nil {
		t.Fatalf("failed to bind: %v", err)
	}

	addr := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}

	if want, got := addr, s.bind; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected bind address:\n- want: %#v\n-  got: %#v",
			want, got)
	}
}

func TestLinuxConnSend(t *testing.T) {
	c, s := testLinuxConn(t, nil)

	req := Message{
		Header: Header{
			Length:   uint32(nlmsgAlign(nlmsgLength(2))),
			Flags:    HeaderFlagsRequest | HeaderFlagsAcknowledge,
			Sequence: 1,
			PID:      uint32(os.Getpid()),
		},
		Data: []byte{0x01, 0x02},
	}

	if err := c.Send(req); err != nil {
		t.Fatalf("error while sending: %v", err)
	}

	// Pad data to 4 bytes as is done when marshaling for later comparison
	req.Data = append(req.Data, 0x00, 0x00)

	to := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}

	if want, got := 0, s.sendto.flags; want != got {
		t.Fatalf("unexpected sendto flags:\n- want: %v\n-  got: %v",
			want, got)
	}
	if want, got := to, s.sendto.to; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected sendto address:\n- want: %#v\n-  got: %#v",
			want, got)
	}

	var out Message
	if err := (&out).UnmarshalBinary(s.sendto.p); err != nil {
		t.Fatalf("failed to unmarshal sendto buffer into message: %v", err)
	}

	if want, got := req, out; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected output message:\n- want: %#v\n-  got: %#v",
			want, got)
	}
}

func TestLinuxConnReceiveInvalidSockaddr(t *testing.T) {
	c, s := testLinuxConn(t, nil)

	s.recvfrom.from = &syscall.SockaddrInet4{}

	_, got := c.Receive()
	if want := errInvalidSockaddr; want != got {
		t.Fatalf("unexpected error:\n-  want: %v\n-  got: %v", want, got)
	}
}

func TestLinuxConnReceiveInvalidFamily(t *testing.T) {
	c, s := testLinuxConn(t, nil)

	s.recvfrom.from = &syscall.SockaddrNetlink{
		// Should always be AF_NETLINK
		Family: syscall.AF_NETLINK + 1,
	}

	_, got := c.Receive()
	if want := errInvalidFamily; want != got {
		t.Fatalf("unexpected error:\n-  want: %v\n-  got: %v", want, got)
	}
}

func TestLinuxConnReceive(t *testing.T) {
	// The request we sent netlink in the previous test; it will be echoed
	// back to us as part of this test
	req := Message{
		Header: Header{
			Length:   uint32(nlmsgAlign(nlmsgLength(4))),
			Flags:    HeaderFlagsRequest | HeaderFlagsAcknowledge,
			Sequence: 1,
			PID:      uint32(os.Getpid()),
		},
		Data: []byte{0x01, 0x02, 0x00, 0x00},
	}
	reqb, err := req.MarshalBinary()
	if err != nil {
		t.Fatalf("failed to marshal request to binary: %v", err)
	}

	res := Message{
		Header: Header{
			// 16 bytes: header
			//  4 bytes: error code
			// 20 bytes: request message
			Length:   uint32(nlmsgAlign(nlmsgLength(24))),
			Type:     HeaderTypeError,
			Sequence: 1,
			PID:      uint32(os.Getpid()),
		},
		// Error code "success", and copy of request
		Data: append([]byte{0x00, 0x00, 0x00, 0x00}, reqb...),
	}
	resb, err := res.MarshalBinary()
	if err != nil {
		t.Fatalf("failed to marshal response to binary: %v", err)
	}

	c, s := testLinuxConn(t, nil)

	from := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}

	s.recvfrom.p = resb
	s.recvfrom.from = from

	msgs, err := c.Receive()
	if err != nil {
		t.Fatalf("failed to receive messages: %v", err)
	}

	if want, got := from, s.recvfrom.from; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected recvfrom address:\n- want: %#v\n-  got: %#v",
			want, got)
	}

	// Expect a MSG_PEEK and then no flags on second call
	if want, got := 2, len(s.recvfrom.flags); want != got {
		t.Fatalf("unexpected number of calls to recvfrom:\n- want: %v\n-  got: %v",
			want, got)
	}
	if want, got := syscall.MSG_PEEK, s.recvfrom.flags[0]; want != got {
		t.Fatalf("unexpected first recvfrom flags:\n- want: %v\n-  got: %v",
			want, got)
	}
	if want, got := 0, s.recvfrom.flags[1]; want != got {
		t.Fatalf("unexpected second recvfrom flags:\n- want: %v\n-  got: %v",
			want, got)
	}

	if want, got := 1, len(msgs); want != got {
		t.Fatalf("unexpected number of messages:\n- want: %v\n-  got: %v",
			want, got)
	}

	if want, got := res, msgs[0]; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected output message:\n- want: %#v\n-  got: %#v",
			want, got)
	}
}

func TestLinuxConnReceiveLargeMessage(t *testing.T) {
	n := os.Getpagesize() * 4

	res := Message{
		Header: Header{
			Length:   uint32(nlmsgAlign(nlmsgLength(n))),
			Type:     HeaderTypeError,
			Sequence: 1,
			PID:      uint32(os.Getpid()),
		},
		Data: make([]byte, n),
	}
	resb, err := res.MarshalBinary()
	if err != nil {
		t.Fatalf("failed to marshal response to binary: %v", err)
	}

	c, s := testLinuxConn(t, nil)

	from := &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}

	s.recvfrom.p = resb
	s.recvfrom.from = from

	if _, err := c.Receive(); err != nil {
		t.Fatalf("failed to receive messages: %v", err)
	}

	// Expect several MSG_PEEK and then no flags
	want := []int{
		syscall.MSG_PEEK,
		syscall.MSG_PEEK,
		syscall.MSG_PEEK,
		syscall.MSG_PEEK,
		0,
	}

	if got := s.recvfrom.flags; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected number recvfrom flags:\n- want: %v\n-  got: %v",
			want, got)
	}
}

func TestLinuxConnIntegration(t *testing.T) {
	const protocolGeneric = 16

	c, err := Dial(protocolGeneric, nil)
	if err != nil {
		t.Fatalf("failed to dial netlink: %v", err)
	}

	// Ask to send us an acknowledgement, which will contain an
	// error code (or success) and a copy of the payload we sent in
	req := Message{
		Header: Header{
			Flags: HeaderFlagsRequest | HeaderFlagsAcknowledge,
		},
	}

	// Perform a request, receive replies, and validate the replies
	msgs, err := c.Execute(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	if want, got := 1, len(msgs); want != got {
		t.Fatalf("unexpected message count from netlink:\n- want: %v\n-  got: %v",
			want, got)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("error closing netlink connection: %v", err)
	}

	m := msgs[0]

	if want, got := 0, int(nlenc.Uint32(m.Data[0:4])); want != got {
		t.Fatalf("unexpected error code:\n- want: %v\n-  got: %v", want, got)
	}

	if want, got := 36, int(m.Header.Length); want != got {
		t.Fatalf("unexpected header length:\n- want: %v\n-  got: %v", want, got)
	}
	if want, got := HeaderTypeError, m.Header.Type; want != got {
		t.Fatalf("unexpected header type:\n- want: %v\n-  got: %v", want, got)
	}
	if want, got := 0, int(m.Header.Flags); want != got {
		t.Fatalf("unexpected header flags:\n- want: %v\n-  got: %v", want, got)
	}
	if want, got := 1, int(m.Header.Sequence); want != got {
		t.Fatalf("unexpected header sequence:\n- want: %v\n-  got: %v", want, got)
	}

	// Skip error code and unmarshal the copy of request sent back by
	// skipping the success code at bytes 0-4
	var reply Message
	if err := (&reply).UnmarshalBinary(m.Data[4:]); err != nil {
		t.Fatalf("failed to unmarshal reply: %v", err)
	}

	if want, got := req.Header.Flags, reply.Header.Flags; want != got {
		t.Fatalf("unexpected copy header flags:\n- want: %v\n-  got: %v", want, got)
	}
	if want, got := 0, int(reply.Header.PID); want != got {
		t.Fatalf("unexpected copy header PID:\n- want: %v\n-  got: %v", want, got)
	}
	if want, got := len(req.Data), len(reply.Data); want != got {
		t.Fatalf("unexpected copy header data length:\n- want: %v\n-  got: %v", want, got)
	}
}

func TestLinuxConnIntegrationConcurrent(t *testing.T) {
	dial := func() *Conn {
		const protocolGeneric = 16

		c, err := Dial(protocolGeneric, nil)
		if err != nil {
			panic(fmt.Sprintf("failed to dial netlink: %v", err))
		}

		return c
	}

	execN := func(c *Conn, n int, wg *sync.WaitGroup) {
		req := Message{
			Header: Header{
				Flags: HeaderFlagsRequest | HeaderFlagsAcknowledge,
			},
		}

		for i := 0; i < n; i++ {
			msgs, err := c.Execute(req)
			if err != nil {
				panic(fmt.Sprintf("failed to execute request: %v", err))
			}

			if l := len(msgs); l != 1 {
				panic(fmt.Sprintf("unexpected number of reply messages: %d", l))
			}
		}

		wg.Done()
	}

	const (
		workers    = 16
		iterations = 10000
	)

	var wg sync.WaitGroup
	wg.Add(workers)

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	conns := make([]*Conn, 0, workers)
	for i := 0; i < workers; i++ {
		c := dial()

		// Assign each worker a random starting sequence number, so that
		// if any messages get crossed, tests will fail immediately
		seq := uint32(r.Int31())
		c.seq = &seq

		conns = append(conns, c)
	}

	for _, c := range conns {
		go execN(c, iterations, &wg)
	}

	wg.Wait()
}

func TestLinuxConnJoinLeaveGroup(t *testing.T) {
	c, s := testLinuxConn(t, nil)

	group := uint32(1)

	if err := c.JoinGroup(group); err != nil {
		t.Fatalf("failed to join group: %v", err)
	}

	if err := c.LeaveGroup(group); err != nil {
		t.Fatalf("failed to leave group: %v", err)
	}

	l := uint32(unsafe.Sizeof(group))

	want := []setSockopt{
		{
			level: solNetlink,
			name:  syscall.NETLINK_ADD_MEMBERSHIP,
			v:     group,
			l:     l,
		},
		{
			level: solNetlink,
			name:  syscall.NETLINK_DROP_MEMBERSHIP,
			v:     group,
			l:     l,
		},
	}

	if got := s.setSockopt; !reflect.DeepEqual(want, got) {
		t.Fatalf("unexpected socket options:\n- want: %v\n-  got: %v",
			want, got)
	}
}

func TestLinuxConnConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		groups uint32
	}{
		{
			name:   "Default Config",
			config: &Config{},
			groups: 0x0,
		},
		{
			name:   "Config with Groups RTMGRP_IPV4_IFADDR",
			config: &Config{Groups: 0x10},
			groups: 0x10,
		},
		{
			name:   "Config with Groups RTMGRP_IPV4_IFADDR | RTMGRP_IPV4_ROUTE",
			config: &Config{Groups: 0x10 | 0x40},
			groups: 0x50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := testLinuxConn(t, tt.config)

			if want, got := tt.groups, c.sa.Groups; want != got {
				t.Fatalf("unexpected error:\n- want: %v\n-  got: %v",
					want, got)
			}
		})
	}
}

func TestConnReceiveErrorLinux(t *testing.T) {
	// Note: using *Conn instead of Linux-only *conn, to test
	// error handling logic in *Conn.Receive

	tests := []struct {
		name string
		req  Message
		rep  [][]Message
		err  error
	}{
		{
			name: "ENOENT",
			rep: [][]Message{{{
				Header: Header{
					Length:   uint32(nlmsgAlign(nlmsgLength(4))),
					Type:     HeaderTypeError,
					Sequence: 1,
					PID:      1,
				},
				// -2, little endian (ENOENT)
				Data: []byte{0xfe, 0xff, 0xff, 0xff},
			}}},
			err: syscall.ENOENT,
		},
		{
			name: "EINTR multipart",
			rep: [][]Message{
				{
					{
						Header: Header{
							Flags: HeaderFlagsMulti,
						},
					},
				},
				{
					{
						Header: Header{
							Type:  HeaderTypeError,
							Flags: HeaderFlagsMulti,
						},
						Data: []byte{0xfc, 0xff, 0xff, 0xff},
					},
				},
			},
			// -4, little endian (EINTR)
			err: syscall.EINTR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, tc := testConn(t)
			tc.receive = tt.rep

			_, err := c.Receive()

			if want, got := tt.err, err; want != got {
				t.Fatalf("unexpected error:\n- want: %v\n-  got: %v",
					want, got)
			}
		})
	}
}

func testLinuxConn(t *testing.T, config *Config) (*conn, *testSocket) {
	s := &testSocket{}
	c, err := bind(s, config)
	if err != nil {
		t.Fatalf("failed to bind: %v", err)
	}

	return c, s
}

type testSocket struct {
	bind   syscall.Sockaddr
	sendto struct {
		p     []byte
		flags int
		to    syscall.Sockaddr
	}
	recvfrom struct {
		// Received from caller
		flags []int
		// Sent to caller
		p    []byte
		from syscall.Sockaddr
	}
	setSockopt []setSockopt

	noopSocket
}

type setSockopt struct {
	level int
	name  int
	v     uint32
	l     uint32
}

func (s *testSocket) Bind(sa syscall.Sockaddr) error {
	s.bind = sa
	return nil
}

func (s *testSocket) Recvfrom(p []byte, flags int) (int, syscall.Sockaddr, error) {
	s.recvfrom.flags = append(s.recvfrom.flags, flags)
	n := copy(p, s.recvfrom.p)

	return n, s.recvfrom.from, nil
}

func (s *testSocket) Sendto(p []byte, flags int, to syscall.Sockaddr) error {
	s.sendto.p = p
	s.sendto.flags = flags
	s.sendto.to = to
	return nil
}

func (s *testSocket) SetSockopt(level, name int, v unsafe.Pointer, l uint32) error {
	s.setSockopt = append(s.setSockopt, setSockopt{
		level: level,
		name:  name,
		v:     *(*uint32)(v),
		l:     l,
	})

	return nil
}

type noopSocket struct{}

func (s *noopSocket) Bind(sa syscall.Sockaddr) error                               { return nil }
func (s *noopSocket) Close() error                                                 { return nil }
func (s *noopSocket) Recvfrom(p []byte, flags int) (int, syscall.Sockaddr, error)  { return 0, nil, nil }
func (s *noopSocket) Sendto(p []byte, flags int, to syscall.Sockaddr) error        { return nil }
func (s *noopSocket) SetSockopt(level, name int, v unsafe.Pointer, l uint32) error { return nil }
