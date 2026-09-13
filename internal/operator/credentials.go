package operator

import (
	"errors"
	"slices"

	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/provider"
	"k8s.io/client-go/kubernetes"
)

// NewWithCredentials binds each named provider to a separate credential scope.
// Providers without explicit Secret values use their deployment authentication.
// The returned cleanup must run only after Run and all other operations stop.
func NewWithCredentials(c Config, k kubernetes.Interface, g *scaleset.Client, credentials map[string]map[string]string) (*Operator, func() error, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	for name := range credentials {
		if _, exists := c.Providers[name]; !exists {
			return nil, nil, errors.New("credentials reference an unknown provider")
		}
	}
	o := New(c, k, g)
	var cleanups []func() error
	cleanup := func() error {
		var failures []error
		for _, close := range cleanups {
			if err := close(); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	}
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		config := c.Providers[name]
		values := credentials[name]
		if len(values) == 0 {
			values = nil
		}
		command, close, err := provider.NewCommand(config, values)
		if err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
		cleanups = append(cleanups, close)
		command.Bootstrap = o.Controller.Providers[name].(*provider.Command).Bootstrap
		o.Controller.Providers[name] = command
	}
	return o, cleanup, nil
}
