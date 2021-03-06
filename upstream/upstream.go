package upstream

import (
	"context"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Upstream struct
type Upstream struct {
	port   string
	host   string
	ips    []*IP
	csum   string
	logger *zap.Logger
	mu     sync.Mutex
	// current resolved record version
	version uint64
	cancel  context.CancelFunc
}

// IP : IP with counter
type IP struct {
	Address string
	// # requerst in busy
	busy int64
	// resolved record version
	version uint64
}

// New :
func New(upstream string, logger *zap.Logger) (*Upstream, error) {
	hostPortSplit := strings.Split(upstream, ":")
	h := hostPortSplit[0]
	p := ""
	if len(hostPortSplit) > 1 {
		p = hostPortSplit[1]
	}

	ctx, cancel := context.WithCancel(context.Background())

	um := &Upstream{
		host:    h,
		port:    p,
		version: 0,
		logger:  logger,
		cancel:  cancel,
	}

	ips, err := um.RefreshIP(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed initial resolv hostname")
	}
	if len(ips) < 1 {
		return nil, errors.New("Could not resolv hostname")
	}
	go um.Run(ctx)
	return um, nil
}

// RefreshIP : resolve hostname
func (u *Upstream) RefreshIP(ctx context.Context) ([]*IP, error) {
	u.mu.Lock()
	u.version++
	u.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, u.host)
	cancel()
	if err != nil {
		return nil, err
	}

	sort.Slice(addrs, func(i, j int) bool {
		return addrs[i].IP.String() > addrs[j].IP.String()
	})

	csumTexts := make([]string, len(addrs))
	ips := make([]*IP, len(addrs))
	for i, ia := range addrs {
		csumTexts[i] = ia.IP.String()
		address := ia.IP.String()
		if u.port != "" {
			address = address + ":" + u.port
		}
		ips[i] = &IP{
			Address: address,
			version: u.version,
			busy:    0,
		}
	}
	csum := strings.Join(csumTexts, ",")
	u.mu.Lock()
	defer u.mu.Unlock()
	if csum != u.csum {
		u.csum = csum
		u.ips = ips
	}

	return ips, nil
}

// Run : resolv hostname in background
func (u *Upstream) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _ = <-ticker.C:
			_, err := u.RefreshIP(ctx)
			if err != nil {
				u.logger.Error("failed refresh ip", zap.Error(err))
			}
		}
	}
}

// GetAll : wild
func (u *Upstream) GetAll() ([]*IP, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if len(u.ips) < 1 {
		return nil, errors.New("No upstream hosts")
	}

	sort.Slice(u.ips, func(i, j int) bool {
		if u.ips[i].busy == u.ips[j].busy {
			return rand.Intn(2) == 0
		}
		return u.ips[i].busy < u.ips[j].busy
	})

	ips := make([]*IP, len(u.ips))
	for i, ip := range u.ips {
		ips[i] = &IP{
			Address: ip.Address,
			version: ip.version,
			busy:    0, // dummy
		}
	}

	return ips, nil
}

// Use : Increment counter
func (u *Upstream) Use(o *IP) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, ip := range u.ips {
		if ip.Address == o.Address && ip.version == o.version {
			u.ips[i].busy = u.ips[i].busy + 1
		}
	}
}

// Release : decrement counter
func (u *Upstream) Release(o *IP) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, ip := range u.ips {
		if ip.Address == o.Address && ip.version == o.version {
			u.ips[i].busy = u.ips[i].busy - 1
		}
	}
}

// Stop : stop upstream updater
func (u *Upstream) Stop() {
	u.cancel()
}
