package ident

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

type fakeWhoIser struct {
	resp *apitype.WhoIsResponse
	err  error

	lastAddr string
}

func (f *fakeWhoIser) WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	f.lastAddr = remoteAddr
	return f.resp, f.err
}

func TestResolve(t *testing.T) {
	userProfile := &tailcfg.UserProfile{
		LoginName:   "alice@example.com",
		DisplayName: "Alice",
	}
	node := &tailcfg.Node{
		Name:         "box.example.",
		ComputedName: "",
		Tags:         nil,
	}
	resp := &apitype.WhoIsResponse{
		Node:        node,
		UserProfile: userProfile,
		CapMap:      tailcfg.PeerCapMap{"cap": []tailcfg.RawMessage{"val"}},
	}

	taggedNode := &tailcfg.Node{
		Name:         "tagged.example.",
		ComputedName: "",
		Tags:         []string{"tag:prod"},
	}
	taggedResp := &apitype.WhoIsResponse{
		Node:        taggedNode,
		UserProfile: userProfile,
		CapMap:      tailcfg.PeerCapMap{"cap": []tailcfg.RawMessage{"val"}},
	}

	tests := []struct {
		name           string
		fakeResp       *apitype.WhoIsResponse
		fakeErr        error
		wantCaller     Caller
		wantCapMap     tailcfg.PeerCapMap
		wantUnknown    bool
		wantErr        bool
		wantRemoteAddr string
	}{
		{
			name:           "user-owned node",
			fakeResp:       resp,
			fakeErr:        nil,
			wantCaller:     Caller{Login: "alice@example.com", DisplayName: "Alice", Node: "box.example", Tags: nil},
			wantCapMap:     tailcfg.PeerCapMap{"cap": []tailcfg.RawMessage{"val"}},
			wantUnknown:    false,
			wantErr:        false,
			wantRemoteAddr: "192.0.2.1:1234",
		},
		{
			name:           "tagged node",
			fakeResp:       taggedResp,
			fakeErr:        nil,
			wantCaller:     Caller{Node: "tagged.example", Tags: []string{"tag:prod"}},
			wantCapMap:     tailcfg.PeerCapMap{"cap": []tailcfg.RawMessage{"val"}},
			wantUnknown:    false,
			wantErr:        false,
			wantRemoteAddr: "192.0.2.2:5678",
		},
		{
			name:           "tagged node tags immutability",
			fakeResp:       taggedResp,
			fakeErr:        nil,
			wantCaller:     Caller{Node: "tagged.example", Tags: []string{"tag:prod"}},
			wantCapMap:     tailcfg.PeerCapMap{"cap": []tailcfg.RawMessage{"val"}},
			wantUnknown:    false,
			wantErr:        false,
			wantRemoteAddr: "192.0.2.3:9999",
		},
		{
			name:           "peer not found",
			fakeResp:       nil,
			fakeErr:        fmt.Errorf("wrapped: %w", local.ErrPeerNotFound),
			wantCaller:     Caller{},
			wantCapMap:     nil,
			wantUnknown:    true,
			wantErr:        true,
			wantRemoteAddr: "192.0.2.4:1111",
		},
		{
			name:           "other error",
			fakeResp:       nil,
			fakeErr:        errors.New("network timeout"),
			wantCaller:     Caller{},
			wantCapMap:     nil,
			wantUnknown:    false,
			wantErr:        true,
			wantRemoteAddr: "192.0.2.5:2222",
		},
		{
			name:           "nil response",
			fakeResp:       nil,
			fakeErr:        nil,
			wantCaller:     Caller{},
			wantCapMap:     nil,
			wantUnknown:    true,
			wantErr:        true,
			wantRemoteAddr: "192.0.2.6:3333",
		},
		{
			name:           "nil node",
			fakeResp:       &apitype.WhoIsResponse{Node: nil, UserProfile: userProfile, CapMap: tailcfg.PeerCapMap{}},
			fakeErr:        nil,
			wantCaller:     Caller{},
			wantCapMap:     nil,
			wantUnknown:    true,
			wantErr:        true,
			wantRemoteAddr: "192.0.2.7:4444",
		},
		{
			name:           "nil user profile",
			fakeResp:       &apitype.WhoIsResponse{Node: node, UserProfile: nil, CapMap: tailcfg.PeerCapMap{}},
			fakeErr:        nil,
			wantCaller:     Caller{},
			wantCapMap:     nil,
			wantUnknown:    true,
			wantErr:        true,
			wantRemoteAddr: "192.0.2.8:5555",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeWhoIser{resp: tt.fakeResp, err: tt.fakeErr}
			ctx := context.Background()

			caller, capMap, err := Resolve(ctx, fake, tt.wantRemoteAddr)

			if fake.lastAddr != tt.wantRemoteAddr {
				t.Errorf("WhoIs called with addr %q; want %q", fake.lastAddr, tt.wantRemoteAddr)
			}

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if tt.wantUnknown {
				if !errors.Is(err, ErrUnknownCaller) {
					t.Errorf("expected error to wrap ErrUnknownCaller, got: %v", err)
				}
			} else if tt.wantErr {
				if errors.Is(err, ErrUnknownCaller) {
					t.Errorf("unexpected ErrUnknownCaller for non-peer-not-found error: %v", err)
				}
			}

			if !tt.wantErr && capMap == nil {
				t.Error("expected CapMap on success, got nil")
			}
			if tt.wantErr && capMap != nil {
				t.Errorf("expected nil CapMap on error, got: %v", capMap)
			}

			if !tt.wantErr {
				if caller.Login != tt.wantCaller.Login {
					t.Errorf("Login = %q; want %q", caller.Login, tt.wantCaller.Login)
				}
				if caller.DisplayName != tt.wantCaller.DisplayName {
					t.Errorf("DisplayName = %q; want %q", caller.DisplayName, tt.wantCaller.DisplayName)
				}
				if caller.Node != tt.wantCaller.Node {
					t.Errorf("Node = %q; want %q", caller.Node, tt.wantCaller.Node)
				}
				if len(caller.Tags) != len(tt.wantCaller.Tags) {
					t.Errorf("Tags length = %d; want %d", len(caller.Tags), len(tt.wantCaller.Tags))
				} else {
					for i := range caller.Tags {
						if caller.Tags[i] != tt.wantCaller.Tags[i] {
							t.Errorf("Tags[%d] = %q; want %q", i, caller.Tags[i], tt.wantCaller.Tags[i])
						}
					}
				}
			}
		})
	}

	// Specific immutability test for tagged nodes
	t.Run("tagged node tags immutability post-resolve", func(t *testing.T) {
		fake := &fakeWhoIser{resp: taggedResp, err: nil}
		ctx := context.Background()

		caller, _, err := Resolve(ctx, fake, "192.0.2.9:6666")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Modify the fake's underlying slice to ensure Caller.Tags is a copy
		fake.resp.Node.Tags[0] = "tag:modified"

		if len(caller.Tags) != 1 || caller.Tags[0] != "tag:prod" {
			t.Errorf("Caller.Tags mutated by fake; got %v", caller.Tags)
		}
	})
}
