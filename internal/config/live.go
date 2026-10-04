package config

import "sync/atomic"

// Live is the config a component reads on every use. The daemon shares one
// Live between all its components and publishes each validated reload to it,
// so a reload reaches everything without a restart and never races a reader.
// A CLI command wraps the config it loaded with NewLive.
type Live struct {
	p atomic.Pointer[Config]
}

// NewLive returns a Live holding cfg.
func NewLive(cfg *Config) *Live {
	l := &Live{}
	l.p.Store(cfg)
	return l
}

// Load returns the latest published config.
func (l *Live) Load() *Config { return l.p.Load() }

// Store publishes cfg to every reader.
func (l *Live) Store(cfg *Config) { l.p.Store(cfg) }
