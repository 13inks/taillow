package ident

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// WhoIser abstracts the local client's WhoIs lookup for testing.
type WhoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

// ErrUnknownCaller indicates the caller could not be identified on the tailnet.
var ErrUnknownCaller = errors.New("caller identity not resolvable on this tailnet")

// Caller represents the resolved identity of a network peer.
type Caller struct {
	Login       string   `json:"login,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Node        string   `json:"node"`
	Tags        []string `json:"tags,omitempty"`
}

// Resolve looks up the caller identified by remoteAddr and returns their identity
// and capability map. It handles partial data and lookup errors according to
// tailnet security policies.
func Resolve(ctx context.Context, w WhoIser, remoteAddr string) (Caller, tailcfg.PeerCapMap, error) {
	resp, err := w.WhoIs(ctx, remoteAddr)
	if err != nil {
		if errors.Is(err, local.ErrPeerNotFound) {
			return Caller{}, nil, fmt.Errorf("%w: %s", ErrUnknownCaller, remoteAddr)
		}
		return Caller{}, nil, fmt.Errorf("looking up caller %s: %w", remoteAddr, err)
	}

	if resp == nil || resp.Node == nil || resp.UserProfile == nil {
		return Caller{}, nil, ErrUnknownCaller
	}

	nodeName := resp.Node.ComputedName
	if nodeName == "" {
		nodeName = strings.TrimSuffix(resp.Node.Name, ".")
	}

	var caller Caller
	caller.Node = nodeName

	if len(resp.Node.Tags) > 0 {
		// Tagged nodes use tags as identity; do not expose user profile data.
		tags := make([]string, len(resp.Node.Tags))
		copy(tags, resp.Node.Tags)
		caller.Tags = tags
	} else {
		// Un-tagged nodes rely on the user profile for identity.
		caller.Login = resp.UserProfile.LoginName
		caller.DisplayName = resp.UserProfile.DisplayName
	}

	return caller, resp.CapMap, nil
}
