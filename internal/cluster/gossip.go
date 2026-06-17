package cluster

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/ratelimit"
	"go.uber.org/zap"
)

// GossipEvent represents an event broadcasted via Gossip.
type GossipEvent struct {
	Action       string    `json:"action"` // "block" or "add_backend"
	IP           string    `json:"ip,omitempty"`
	BlockedUntil time.Time `json:"blocked_until,omitempty"`
	BackendURL   string    `json:"backend_url,omitempty"`
}

// Invalidates checks if this broadcast invalidates another queued broadcast.
func (e *GossipEvent) Invalidates(other memberlist.Broadcast) bool {
	if o, ok := other.(*GossipEvent); ok {
		if o.Action == e.Action && e.Action == "block" {
			if o.IP == e.IP {
				return e.BlockedUntil.After(o.BlockedUntil)
			}
		}
	}
	return false
}

// Message serializes the broadcast payload.
func (e *GossipEvent) Message() []byte {
	data, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	return data
}

// Finished is invoked when the message will no longer be broadcast.
func (e *GossipEvent) Finished() {
}

// GossipDelegate implements the memberlist.Delegate interface.
type GossipDelegate struct {
	Limiter    *ratelimit.RateLimiter
	Pool       *proxy.ServerPool
	Broadcasts *memberlist.TransmitLimitedQueue
}

// NodeMeta returns node metadata (not used, returns empty).
func (d *GossipDelegate) NodeMeta(limit int) []byte {
	return []byte{}
}

// NotifyMsg processes received gossip broadcasts.
func (d *GossipDelegate) NotifyMsg(msg []byte) {
	var event GossipEvent
	if err := json.Unmarshal(msg, &event); err != nil {
		logger.Log.Error("Failed to unmarshal received gossip message", zap.Error(err))
		return
	}
	switch event.Action {
	case "block":
		logger.Log.Info("Gossip cluster sync: received remote IP block",
			zap.String("ip", event.IP),
			zap.Time("blocked_until", event.BlockedUntil),
		)
		d.Limiter.BlockIP(event.IP, event.BlockedUntil)
	case "add_backend":
		logger.Log.Info("Gossip cluster sync: received remote backend registration",
			zap.String("url", event.BackendURL),
		)
		if d.Pool != nil {
			alreadyExists := false
			for _, b := range d.Pool.GetBackends() {
				if b.URL.String() == event.BackendURL {
					alreadyExists = true
					break
				}
			}
			if !alreadyExists {
				var timeout time.Duration
				backends := d.Pool.GetBackends()
				if len(backends) > 0 {
					timeout = backends[0].TripDuration
				}
				_, err := proxy.RegisterBackendURL(d.Pool, event.BackendURL, timeout)
				if err != nil {
					logger.Log.Error("Failed to register dynamic backend via gossip sync", zap.String("url", event.BackendURL), zap.Error(err))
				}
			}
		}
	}
}

// GetBroadcasts returns user data broadcasts to send.
func (d *GossipDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	if d.Broadcasts == nil {
		return nil
	}
	return d.Broadcasts.GetBroadcasts(overhead, limit)
}

// LocalState serializes local active IP blocks for state push/pull sync.
func (d *GossipDelegate) LocalState(join bool) []byte {
	blocks := d.Limiter.GetActiveBlocks()
	data, err := json.Marshal(blocks)
	if err != nil {
		logger.Log.Error("Failed to marshal local state for gossip sync", zap.Error(err))
		return []byte{}
	}
	return data
}

// MergeRemoteState merges remote state after push/pull sync.
func (d *GossipDelegate) MergeRemoteState(buf []byte, join bool) {
	if len(buf) == 0 {
		return
	}
	var remoteBlocks map[string]time.Time
	if err := json.Unmarshal(buf, &remoteBlocks); err != nil {
		logger.Log.Error("Failed to merge remote gossip state", zap.Error(err))
		return
	}
	logger.Log.Info("Gossip cluster sync: merging state from remote node",
		zap.Int("count", len(remoteBlocks)),
	)
	d.Limiter.MergeBlocks(remoteBlocks)
}

// GossipService manages the lifecycle of the memberlist node.
type GossipService struct {
	list       *memberlist.Memberlist
	delegate   *GossipDelegate
	broadcasts *memberlist.TransmitLimitedQueue
}

// StartGossipService configures, initializes, and starts the gossip membership node.
func StartGossipService(nodeName string, bindAddr string, bindPort int, joinAddr string, limiter *ratelimit.RateLimiter, pool *proxy.ServerPool) (*GossipService, error) {
	config := memberlist.DefaultLANConfig()
	config.Name = nodeName
	if bindAddr != "" {
		config.BindAddr = bindAddr
	}
	if bindPort != 0 {
		config.BindPort = bindPort
	}

	broadcasts := &memberlist.TransmitLimitedQueue{
		NumNodes: func() int {
			return 1
		},
		RetransmitMult: 3,
	}

	delegate := &GossipDelegate{
		Limiter:    limiter,
		Pool:       pool,
		Broadcasts: broadcasts,
	}
	config.Delegate = delegate

	list, err := memberlist.Create(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create memberlist node: %w", err)
	}

	// Dynamically link node count function
	broadcasts.NumNodes = func() int {
		return list.NumMembers()
	}
	delegate.Broadcasts = broadcasts

	if joinAddr != "" {
		logger.Log.Info("Joining existing gossip cluster", zap.String("join_addr", joinAddr))
		_, err = list.Join([]string{joinAddr})
		if err != nil {
			logger.Log.Error("Failed to join gossip cluster, starting as standalone seed", zap.Error(err))
		}
	} else {
		logger.Log.Info("No cluster join address provided; starting as seed node")
	}

	// Register the RateLimiter callback to broadcast events when rate-limited
	limiter.SetBroadcastCallback(func(ip string, blockedUntil time.Time) {
		logger.Log.Info("Queueing IP block broadcast to gossip cluster", zap.String("ip", ip))
		broadcasts.QueueBroadcast(&GossipEvent{
			Action:       "block",
			IP:           ip,
			BlockedUntil: blockedUntil,
		})
	})

	return &GossipService{
		list:       list,
		delegate:   delegate,
		broadcasts: broadcasts,
	}, nil
}

// Shutdown leaves the cluster and releases socket bindings.
func (g *GossipService) Shutdown() error {
	if g.list != nil {
		logger.Log.Info("Leaving cluster membership...")
		err := g.list.Leave(1 * time.Second)
		if err != nil {
			logger.Log.Error("Failed to leave gossip cluster gracefully", zap.Error(err))
		}
		return g.list.Shutdown()
	}
	return nil
}

// BroadcastBackendAdd broadcasts a backend addition event to the cluster.
func (g *GossipService) BroadcastBackendAdd(url string) {
	if g.broadcasts != nil {
		logger.Log.Info("Queueing backend add broadcast to gossip cluster", zap.String("url", url))
		g.broadcasts.QueueBroadcast(&GossipEvent{
			Action:     "add_backend",
			BackendURL: url,
		})
	}
}
