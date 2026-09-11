package tundev

import (
	"bytes"
	"os"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
)

// fakeTun records how the adapter calls the underlying device.
//
// The real device needs CAP_NET_ADMIN, so this is where the offset contract
// gets checked: wireguard-go uses the bytes before the offset as scratch space
// and rejects anything under 10 with "invalid offset".
type fakeTun struct {
	wroteOffset int
	wrote       []byte

	readPacket  []byte
	readPackets [][]byte
	readOffset  int
	readErr     error
	batchSize   int
	reads       int

	closed bool
}

func (f *fakeTun) File() *os.File           { return nil }
func (f *fakeTun) MTU() (int, error)        { return 1400, nil }
func (f *fakeTun) Name() (string, error)    { return "faketun0", nil }
func (f *fakeTun) Events() <-chan tun.Event { return nil }
func (f *fakeTun) Close() error             { f.closed = true; return nil }
func (f *fakeTun) BatchSize() int {
	if f.batchSize > 0 {
		return f.batchSize
	}

	return 1
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	f.wroteOffset = offset
	f.wrote = append([]byte(nil), bufs[0][offset:]...)

	return len(bufs), nil
}

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	f.readOffset = offset
	f.reads++

	if f.readErr != nil {
		err := f.readErr
		f.readErr = nil

		return 0, err
	}

	packets := f.readPackets
	if packets == nil {
		packets = [][]byte{f.readPacket}
	}

	if len(packets) > len(bufs) {
		return 0, tun.ErrTooManySegments
	}

	for i, packet := range packets {
		copy(bufs[i][offset:], packet)
		sizes[i] = len(packet)
	}

	return len(packets), nil
}

func TestWriteReservesTheHeadroomTheLibraryRequires(t *testing.T) {
	fake := &fakeTun{}
	device := newAdapter(fake, headroom+1400+slack)

	packet := []byte{0x45, 0x00, 0x00, 0x28, 0xde, 0xad}

	n, err := device.Write(packet)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(packet) {
		t.Errorf("wrote %d bytes, want %d", n, len(packet))
	}

	// Ten is the size of the virtio-net header on Linux; a smaller offset is
	// refused outright, which is the failure this pins down.
	if fake.wroteOffset < 10 {
		t.Errorf("offset = %d, want at least 10", fake.wroteOffset)
	}
	if !bytes.Equal(fake.wrote, packet) {
		t.Errorf("device received % x, want % x", fake.wrote, packet)
	}
}

func TestReadReturnsThePacketWithoutHeadroom(t *testing.T) {
	fake := &fakeTun{readPacket: []byte{0x45, 0x00, 0x11, 0x22}}
	device := newAdapter(fake, headroom+1400+slack)

	buf := make([]byte, 2048)
	n, err := device.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if fake.readOffset < 10 {
		t.Errorf("offset = %d, want at least 10", fake.readOffset)
	}
	if !bytes.Equal(buf[:n], fake.readPacket) {
		t.Errorf("read % x, want % x", buf[:n], fake.readPacket)
	}
}

func TestWriteReusesItsBufferAcrossPackets(t *testing.T) {
	fake := &fakeTun{}
	device := newAdapter(fake, headroom+1400+slack)

	// A short packet used to shrink the buffer permanently, forcing a fresh
	// allocation for every larger packet after it.
	if _, err := device.Write([]byte{1, 2, 3}); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	before := cap(device.writeBuf)

	large := make([]byte, 1200)
	if _, err := device.Write(large); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	if cap(device.writeBuf) != before {
		t.Errorf("buffer capacity changed from %d to %d for a packet that fits", before, cap(device.writeBuf))
	}
	if len(fake.wrote) != len(large) {
		t.Errorf("device received %d bytes, want %d", len(fake.wrote), len(large))
	}
}

func TestWriteGrowsForAnOversizedPacket(t *testing.T) {
	fake := &fakeTun{}
	device := newAdapter(fake, headroom+64)

	oversized := make([]byte, 4000)
	for i := range oversized {
		oversized[i] = byte(i)
	}

	if _, err := device.Write(oversized); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(fake.wrote, oversized) {
		t.Error("an oversized packet was truncated instead of being carried whole")
	}
}

func TestReadRefusesAPacketLargerThanTheCallerBuffer(t *testing.T) {
	fake := &fakeTun{readPacket: make([]byte, 500)}
	device := newAdapter(fake, headroom+1400+slack)

	// Copying into a short buffer would silently truncate the packet.
	if _, err := device.Read(make([]byte, 100)); err == nil {
		t.Fatal("expected an error when the packet does not fit")
	}
}

func TestCreateSizesBuffersToTheDeviceBatch(t *testing.T) {
	// Supplying fewer buffers than the device's batch size makes a coalesced
	// read fail outright with ErrTooManySegments, which is what took the
	// tunnel down after a few seconds of real traffic.
	fake := &fakeTun{batchSize: 128}
	device := newAdapter(fake, headroom+1400+slack)

	if len(device.readBufs) != 128 {
		t.Errorf("allocated %d read buffers, want %d", len(device.readBufs), fake.BatchSize())
	}
	if len(device.readSizes) != len(device.readBufs) {
		t.Errorf("sizes slice holds %d entries, buffers %d", len(device.readSizes), len(device.readBufs))
	}
}

func TestReadDrainsABatchOnePacketAtATime(t *testing.T) {
	fake := &fakeTun{
		batchSize: 4,
		readPackets: [][]byte{
			{0x45, 0x01},
			{0x45, 0x02, 0x02},
			{0x45, 0x03, 0x03, 0x03},
		},
	}
	device := newAdapter(fake, headroom+1400+slack)

	buf := make([]byte, 2048)
	for i, want := range fake.readPackets {
		n, err := device.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		if !bytes.Equal(buf[:n], want) {
			t.Errorf("packet %d = % x, want % x", i+1, buf[:n], want)
		}
	}

	// All three came from a single read of the device.
	if fake.reads != 1 {
		t.Errorf("the device was read %d times for one batch, want 1", fake.reads)
	}
}

func TestReadSurvivesASegmentOverflow(t *testing.T) {
	// The library documents this error as recoverable, so it must not take the
	// connection down.
	fake := &fakeTun{batchSize: 2, readErr: tun.ErrTooManySegments, readPacket: []byte{0x45, 0x09}}
	device := newAdapter(fake, headroom+1400+slack)

	buf := make([]byte, 2048)

	n, err := device.Read(buf)
	if err != nil {
		t.Fatalf("a recoverable overflow was reported as fatal: %v", err)
	}
	if n != 0 {
		t.Errorf("returned %d bytes for a lost batch, want 0", n)
	}
	if device.Dropped() != 1 {
		t.Errorf("dropped counter = %d, want 1", device.Dropped())
	}

	// Reading must keep working afterwards.
	n, err = device.Read(buf)
	if err != nil {
		t.Fatalf("the next read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], fake.readPacket) {
		t.Errorf("read % x, want % x", buf[:n], fake.readPacket)
	}
}
