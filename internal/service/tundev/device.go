// Package tundev creates the virtual network interface the tunnel delivers
// packets to.
//
// It is a thin seam over wireguard-go's tun package, which already provides one
// API across Linux, macOS and Windows (Wintun). Keeping it behind an interface
// lets everything above be tested without CAP_NET_ADMIN, which is otherwise
// required to create a device at all.
package tundev

import (
	"github.com/sorintlab/errors"
	"golang.zx2c4.com/wireguard/tun"
)

// headroom is the space reserved in front of every packet handed to
// wireguard-go.
//
// Its Read and Write take an offset into each buffer and use the bytes before
// it as scratch space: on Linux they hold the virtio-net header, and a smaller
// offset is rejected outright with "invalid offset". The value matches what
// wireguard-go itself passes (MessageTransportHeaderSize), comfortably above
// the 10-byte minimum that header needs.
const headroom = 16

// slack covers the difference between the interface MTU and the largest frame
// the kernel may hand over.
const slack = 128

// Device is a virtual interface packets can be read from and written to.
//
// Read and Write take plain packets: the headroom the underlying library wants
// is handled here so callers never have to know about it.
type Device interface {
	// Name is the interface name the operating system assigned.
	Name() (string, error)
	// Read fills packet with one IP packet and returns its length.
	Read(packet []byte) (int, error)
	// Write sends one IP packet.
	Write(packet []byte) (int, error)
	// MTU is the interface's maximum transmission unit.
	MTU() (int, error)
	Close() error
}

// Create makes a new TUN interface.
//
// name is a hint; the operating system may assign a different one, so callers
// must read it back with Name rather than assuming.
func Create(name string, mtu int) (Device, error) {
	device, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, errors.Wrapf(err, "creating the tun interface")
	}

	return newAdapter(device, headroom+mtu+slack), nil
}

// newAdapter wraps a tun.Device. Split out from Create so the adapter can be
// exercised against a fake device, which is the only way to check the offset
// handling without the privileges a real interface needs.
func newAdapter(device tun.Device, capacity int) *wireguardDevice {
	// One read can return many packets: with segmentation offload the kernel
	// hands over a coalesced batch, and wireguard-go splits it across the
	// buffers it is given. Supplying fewer than BatchSize fails the read
	// outright with ErrTooManySegments.
	batch := device.BatchSize()
	if batch < 1 {
		batch = 1
	}

	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, capacity)
	}

	return &wireguardDevice{
		device:    device,
		readBufs:  bufs,
		readSizes: make([]int, batch),
		writeBuf:  make([]byte, capacity),
		writeBufs: make([][]byte, 1),
	}
}

// wireguardDevice adapts wireguard-go's batched, offset-based API to one plain
// packet at a time.
//
// The read and write buffers are separate because the two directions are
// pumped by different goroutines; the library serialises calls to each of its
// own methods internally.
type wireguardDevice struct {
	device    tun.Device
	readBufs  [][]byte
	readSizes []int
	// readCount is how many packets the last read produced, and readNext the
	// index of the one to hand back. A batch is drained across calls so the
	// caller keeps seeing one packet at a time.
	readCount int
	readNext  int
	// writeBuf holds the full allocation; writeBufs[0] is re-sliced from it on
	// every write. Keeping them apart preserves the capacity, which slicing
	// writeBufs[0] in place would shrink away.
	writeBuf  []byte
	writeBufs [][]byte
	// dropped counts batches lost to ErrTooManySegments, which should stay at
	// zero now that the buffers match BatchSize.
	dropped int
}

func (d *wireguardDevice) Name() (string, error) {
	name, err := d.device.Name()

	return name, errors.Wrapf(err, "reading the interface name")
}

func (d *wireguardDevice) MTU() (int, error) {
	mtu, err := d.device.MTU()

	return mtu, errors.Wrapf(err, "reading the interface MTU")
}

func (d *wireguardDevice) Close() error {
	return errors.Wrapf(d.device.Close(), "closing the tun device")
}

func (d *wireguardDevice) Read(packet []byte) (int, error) {
	if d.readNext >= d.readCount {
		count, err := d.device.Read(d.readBufs, d.readSizes, headroom)
		if err != nil {
			// The library documents this one as recoverable: a batch that
			// overflowed the buffers is lost, but the interface is still fine
			// and reads must continue. Reporting no packet lets the caller
			// come straight back.
			if errors.Is(err, tun.ErrTooManySegments) {
				d.dropped++
				return 0, nil
			}

			return 0, errors.Wrapf(err, "reading from the tun device")
		}

		d.readCount = count
		d.readNext = 0

		if count == 0 {
			return 0, nil
		}
	}

	index := d.readNext
	size := d.readSizes[index]
	if size > len(packet) {
		return 0, errors.Errorf("read a %d byte packet into a %d byte buffer", size, len(packet))
	}

	copy(packet, d.readBufs[index][headroom:headroom+size])
	d.readNext++

	return size, nil
}

// Dropped reports how many read batches were lost to buffer overflow.
func (d *wireguardDevice) Dropped() int { return d.dropped }

func (d *wireguardDevice) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}

	needed := headroom + len(packet)
	if needed > len(d.writeBuf) {
		// A packet larger than the interface MTU still has to go somewhere
		// rather than being silently dropped.
		d.writeBuf = make([]byte, needed)
	}

	copy(d.writeBuf[headroom:needed], packet)
	d.writeBufs[0] = d.writeBuf[:needed]

	if _, err := d.device.Write(d.writeBufs, headroom); err != nil {
		return 0, errors.Wrapf(err, "writing to the tun device")
	}

	return len(packet), nil
}
