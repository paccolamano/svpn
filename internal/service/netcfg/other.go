//go:build !linux

package netcfg

import "github.com/sorintlab/errors"

// New returns a configurator that refuses to run.
//
// macOS needs ifconfig/route plus scutil for DNS, and Windows needs the IP
// Helper API or netsh. Both are real work with different failure modes, and
// pretending otherwise would leave a connection half-configured.
func New() Configurator { return unsupported{} }

type unsupported struct{}

func (unsupported) Apply(Settings) error {
	return errors.New("network configuration is not implemented on this platform yet")
}

func (unsupported) Revert() error { return nil }
