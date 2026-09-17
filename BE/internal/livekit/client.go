// Package livekit wraps the small surface of LiveKit that ku needs:
// minting scoped access tokens and driving the RoomService API for
// participant counts and room teardown.
package livekit

import (
	"context"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"
)

const tokenTTL = 12 * time.Hour

type Client struct {
	roomService *lksdk.RoomServiceClient
	url         string
	apiKey      string
	apiSecret   string
}

func NewClient(url, apiKey, apiSecret string) *Client {
	return &Client{
		roomService: lksdk.NewRoomServiceClient(url, apiKey, apiSecret),
		url:         url,
		apiKey:      apiKey,
		apiSecret:   apiSecret,
	}
}

// ServerURL returns the URL browsers should connect to for signaling.
func (c *Client) ServerURL() string {
	return c.url
}

// PresenterToken mints a token that may publish into room (and cannot
// subscribe — the presenter never consumes their own echo).
func (c *Client) PresenterToken(room, identity, name string) (string, error) {
	yes, no := true, false
	grant := &auth.VideoGrant{
		RoomJoin:       true,
		Room:           room,
		CanPublish:     &yes,
		CanSubscribe:   &no,
		CanPublishData: &no,
	}
	return c.mint(grant, identity, name)
}

// ViewerToken mints a subscribe-only token for room.
func (c *Client) ViewerToken(room, identity, name string) (string, error) {
	yes, no := true, false
	grant := &auth.VideoGrant{
		RoomJoin:       true,
		Room:           room,
		CanPublish:     &no,
		CanSubscribe:   &yes,
		CanPublishData: &no,
	}
	return c.mint(grant, identity, name)
}

func (c *Client) mint(grant *auth.VideoGrant, identity, name string) (string, error) {
	accessToken := auth.NewAccessToken(c.apiKey, c.apiSecret)
	return accessToken.
		SetVideoGrant(grant).
		SetIdentity(identity).
		SetName(name).
		SetValidFor(tokenTTL).
		ToJWT()
}

// ViewerCount returns the number of connected participants other than the
// presenter identified by presenterIdentity. Returns an error when the room
// does not exist (callers treat that as "share is over").
func (c *Client) ViewerCount(ctx context.Context, room, presenterIdentity string) (int, error) {
	resp, err := c.roomService.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, p := range resp.GetParticipants() {
		if p.GetIdentity() != presenterIdentity {
			count++
		}
	}
	return count, nil
}

// DeleteRoom forcibly removes every participant and the room itself. A
// missing room is not an error — the outcome (no room) is what we want.
func (c *Client) DeleteRoom(ctx context.Context, room string) error {
	_, err := c.roomService.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: room})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// RoomExists reports whether a LiveKit room is currently live. A transport
// failure is reported as an error so callers can distinguish "room gone"
// from "cannot tell".
func (c *Client) RoomExists(ctx context.Context, room string) (bool, error) {
	_, err := c.roomService.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isNotFound(err error) bool {
	twerr, ok := err.(twirp.Error)
	return ok && twerr.Code() == twirp.NotFound
}

// IsRoomGone reports whether err means the room no longer exists (as
// opposed to a transport failure reaching LiveKit at all).
func IsRoomGone(err error) bool {
	return err != nil && isNotFound(err)
}
